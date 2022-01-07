package main

// MUCH OF THE CODE IN THIS FILE IS RIPPED STRAIGHT FROM github.com/livegrep/cmd/livegrep-github-reindexer/main.go

import (
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"path"
	"strings"
	"sync"

	"github.com/google/go-github/v41/github"
	"github.com/xvandish/livegrep-fragment/src/proto/config"

	"golang.org/x/net/context"
)

const BLDeprecatedMessage = "This flag has been deprecated and will be removed in a future release. Please switch to the '-ignorelist' option."

var (
	flagCodesearch   = flag.String("codesearch", "", "Path to the `codesearch` binary")
	flagFetchReindex = flag.String("fetch-reindex", "", "Path to the `livegrep-fetch-reindex` binary")
	flagApiBaseUrl   = flag.String("api-base-url", "https://api.github.com/", "Github API base url")
	flagGithubKey    = flag.String("github-key", os.Getenv("GITHUB_KEY"), "Github API key")
	flagRepoDir      = flag.String("dir", "repos", "Directory to store repos")
	flagIgnorelist   = flag.String("ignorelist", "", "File containing a list of repositories to ignore when indexing")
	flagDeprecatedBL = flag.String("blacklist", "", "[DEPRECATED] "+BLDeprecatedMessage)
	flagIndexPath    = dynamicDefault{
		display: "${dir}/livegrep.idx",
		fn:      func() string { return path.Join(*flagRepoDir, "livegrep.idx") },
	}
	flagRevision                = flag.String("revision", "HEAD", "git revision to index")
	flagUrlPattern              = flag.String("url-pattern", "https://github.com/{name}/blob/{version}/{path}#L{lno}", "when using the local frontend fileviewer, this string will be used to construt a link to the file source on github")
	flagName                    = flag.String("name", "livegrep index", "The name to be stored in the index file")
	flagNumRepoUpdateWorkers    = flag.String("num-repo-update-workers", "8", "Number of workers fetch-reindex will use to update repositories")
	flagRevparse                = flag.Bool("revparse", true, "whether to `git rev-parse` the provided revision in generated links")
	flagForks                   = flag.Bool("forks", true, "whether to index repositories that are github forks, and not original repos")
	flagArchived                = flag.Bool("archived", false, "whether to index repositories that are archived on github")
	flagHTTP                    = flag.Bool("http", false, "clone repositories over HTTPS instead of SSH")
	flagHTTPUsername            = flag.String("http-user", "git", "Override the username to use when cloning over https")
	flagInstallation            = flag.Bool("installation-token", false, "Treat the API key as a Github Application Installation Key when cloning")
	flagDepth                   = flag.Int("depth", 0, "clone repository with specify --depth=N depth.")
	flagSkipMissing             = flag.Bool("skip-missing", false, "skip repositories where the specified revision is missing")
	flagMaxConcurrentGHRequests = flag.Int("max-concurrent-gh-requests", 1, "Applied per org/user. If fetching 2 orgs, you will have 2x{yourInput} network calls possible at a time")

	flagRepos = stringList{}
	flagOrgs  = stringList{}
	flagUsers = stringList{}
)

// 1. Fetch all repos and write config to disk
// 2. TODO
// 3. TODO
// 4. TODO
//

const Workers = 8

func main() {
	flag.Parse()
	log.SetFlags(0) // sets output flags for standard logger

	if *flagDeprecatedBL != "" {
		log.Fatalln(BLDeprecatedMessage)
	}

	if flagRepos.strings == nil &&
		flagOrgs.strings == nil &&
		flagUsers.strings == nil {
		log.Fatal("You must specify at least one repo or organization to index")
	}

	// look into removing this, not sure if I'd use it
	if *flagInstallation {
		if *flagGithubKey == "" {
			log.Fatal("-installation-key requires passing a github key, via either -github-key or $GITHUB_KEY")
		}
		*flagHTTP = true
		*flagHTTPUsername = "x-access-token"
	}

	var ignorelist map[string]struct{}
	if *flagIgnorelist != "" {
		var err error
		ignorelist, err = loadIgnorelist(*flagIgnorelist)
		if err != nil {
			log.Fatalf("loading %s: %s", *flagIgnorelist, err)
		}
	}

}

type ReposByName []*github.Repository

func (r ReposByName) Len() int           { return len(r) }
func (r ReposByName) Swap(i, j int)      { r[i], r[j] = r[j], r[i] }
func (r ReposByName) Less(i, j int) bool { return *r[i].FullName < *r[j].FullName }

func loadIgnorelist(path string) (map[string]struct{}, error) {
	data, err := ioutil.ReadFile(path)
	if err != nil {
		return nil, err
	}

	lines := strings.Split(string(data), "\n")
	out := make(map[string]struct{}, len(lines))
	for _, l := range lines {
		out[l] = struct{}{}
	}
	return out, nil
}

type loadJob struct {
	obj string
	get func(*github.Client, string) ([]*github.Repository, error)
}

type maybeRepo struct {
	repos []*github.Repository
	err   error
}

func loadRepos(
	client *github.Client,
	repos []string,
	orgs []string,
	users []string) ([]*github.Repository, error) {

	jobc := make(chan loadJob)
	done := make(chan struct{})
	repoc := make(chan maybeRepo)

	var jobs []loadJob
	for _, repo := range repos {
		jobs = append(jobs, loadJob{repo, getOneRepo})
	}
	for _, org := range orgs {
		jobs = append(jobs, loadJob{org, getOrgRepos})
	}
	for _, user := range users {
		jobs = append(jobs, loadJob{user, getUserRepos})
	}
	go func() {
		defer close(jobc)
		for _, j := range jobs {
			select {
			case jobc <- j:
			case <-done:
				return
			}
		}
	}()
	var wg sync.WaitGroup
	wg.Add(Workers)
	for i := 0; i < Workers; i++ {
		go func() {
			runJobs(client, jobc, done, repoc)
			wg.Done()
		}()
	}
	go func() {
		wg.Wait()
		close(repoc)
	}()
	var out []*github.Repository
	for repo := range repoc {
		if repo.err != nil {
			close(done)
			return nil, repo.err
		}
		out = append(out, repo.repos...)
	}

	return out, nil
}

func runJobs(client *github.Client, jobc <-chan loadJob, done <-chan struct{}, out chan<- maybeRepo) {
	for {
		var job loadJob
		var ok bool
		select {
		case job, ok = <-jobc:
			if !ok {
				return
			}
		case <-done:
			return
		}
		var res maybeRepo
		res.repos, res.err = job.get(client, job.obj)
		select {
		case out <- res:
		case <-done:
			return
		}
	}
}

func filterRepos(repos []*github.Repository,
	ignorelist map[string]struct{},
	excludeForks bool, excludeArchived bool) []*github.Repository {
	var out []*github.Repository

	for _, r := range repos {
		if excludeForks && r.Fork != nil && *r.Fork {
			log.Printf("Excluding fork %s...", *r.FullName)
			continue
		}
		if excludeArchived && r.Archived != nil && *r.Archived {
			log.Printf("Excluding archived %s...", *r.FullName)
			continue
		}
		if ignorelist != nil {
			if _, ok := ignorelist[*r.FullName]; ok {
				continue
			}
		}
		out = append(out, r)
	}

	return out
}

func getOneRepo(client *github.Client, repo string) ([]*github.Repository, error) {
	bits := strings.SplitN(repo, "/", 2)
	if len(bits) != 2 {
		return nil, fmt.Errorf("Bad repository: %s", repo)
	}

	ghRepo, _, err := client.Repositories.Get(context.TODO(), bits[0], bits[1])
	if err != nil {
		return nil, err
	}
	return []*github.Repository{ghRepo}, nil
}

type IndexedResponse struct {
	Page  int
	Org   string
	Repos []*github.Repository
	err   error
}

func callGitHubConcurrently(initialResp *github.Response, concurrencyLimit int, firstResult []*github.Repository, gClient *github.Client, method string, org string, user string) ([]*github.Repository, error) {
	pagesToCall := initialResp.LastPage - 1

	// create the matrix of results and add the first one - this is so we can maintain order
	// which unfortunately takes an extra O(n) pass
	resultsMatrix := make([][]*github.Repository, pagesToCall+1)
	resultsMatrix[0] = firstResult

	semaphores := make(chan bool, concurrencyLimit)
	resStream := make(chan *IndexedResponse, pagesToCall)
	var wg sync.WaitGroup

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	for i := 1; i <= pagesToCall; i++ {
		wg.Add(1)

		go func(ctx context.Context, page int, c chan *IndexedResponse, s chan bool, w *sync.WaitGroup) {
			s <- true // aquire semaphore
			defer w.Done()

			var repos []*github.Repository
			var err error
			if method == "org" {
				repos, _, err = gClient.Repositories.ListByOrg(ctx, org, &github.RepositoryListByOrgOptions{
					ListOptions: github.ListOptions{PerPage: 100, Page: page},
				})
			} else if method == "user" {
				repos, _, err = gClient.Repositories.List(ctx, user, &github.RepositoryListOptions{
					ListOptions: github.ListOptions{PerPage: 100, Page: page},
				})
			}

			c <- &IndexedResponse{
				Page:  page,
				Repos: repos,
				Org:   org,
				err:   err,
			}
			<-s // release semaphore
		}(ctx, i+1, resStream, semaphores, &wg) // + 1 because pages are 1 based, and we already called 1st to start with
	}

	// close the channel in the background
	go func() {
		wg.Wait()
		close(resStream)
		close(semaphores)
	}()

	for res := range resStream {
		if res.err != nil {
			return nil, res.err // cancel will be called after this early return
		}
		resultsMatrix[res.Page-1] = res.Repos // Page index is 1 based
	}

	// Now flatten the matrix and return it
	var buf []*github.Repository
	for _, res := range resultsMatrix {
		buf = append(buf, res...)
	}

	return buf, nil
}

func getOrgRepos(client *github.Client, org string) ([]*github.Repository, error) {
	log.Printf("Fetching repositories for organization: %s", org)

	opt := &github.RepositoryListByOrgOptions{
		ListOptions: github.ListOptions{PerPage: 100},
	}
	repos, resp, err := client.Repositories.ListByOrg(context.TODO(), org, opt)

	if err != nil {
		return nil, err
	} else if resp.FirstPage == resp.LastPage { // if no more pages, return early
		return repos, nil
	}

	// when flagMaxConcurrentGHRequests is 1 (default), behaves synchronously
	return callGitHubConcurrently(resp, *flagMaxConcurrentGHRequests, repos, client, "org", org, "")
}

func getUserRepos(client *github.Client, user string) ([]*github.Repository, error) {
	log.Printf("Fetching repositories for user: %s", user)

	opt := &github.RepositoryListOptions{
		ListOptions: github.ListOptions{PerPage: 100},
	}
	repos, resp, err := client.Repositories.List(context.TODO(), user, opt)
	if err != nil {
		return nil, err
	} else if resp.FirstPage == resp.LastPage { // if no more pages, return early
		return repos, nil
	}

	// when flagMaxConcurrentGHRequests is 1 (default), behaves synchronously
	return callGitHubConcurrently(resp, *flagMaxConcurrentGHRequests, repos, client, "user", "", user)
}

func writeConfig(config []byte, file string) error {
	dir := path.Dir(file)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	return ioutil.WriteFile(file, config, 0644)
}

func buildConfig(name string,
	dir string,
	repos []*github.Repository,
	revision string) ([]byte, error) {
	cfg := config.IndexSpec{
		Name: name,
	}

	for _, r := range repos {
		if *flagSkipMissing {
			cmd := exec.Command("git",
				"--git-dir",
				path.Join(dir, *r.FullName),
				"rev-parse",
				"--verify",
				revision,
			)
			if e := cmd.Run(); e != nil {
				log.Printf("Skipping missing revision repo=%s rev=%s",
					*r.FullName, revision,
				)
				continue
			}
		}
		var remote string
		if *flagHTTP {
			remote = *r.CloneURL
		} else {
			remote = *r.SSHURL
		}

		var password_env string
		if *flagGithubKey != "" {
			password_env = "GITHUB_KEY"
		}

		cfg.Repositories = append(cfg.Repositories, &config.RepoSpec{
			Path:      path.Join(dir, *r.FullName),
			Name:      *r.FullName,
			Revisions: []string{revision},
			Metadata: &config.Metadata{
				Github:     *r.HTMLURL,
				Remote:     remote,
				UrlPattern: *flagUrlPattern,
			},
			CloneOptions: &config.CloneOptions{
				Depth:       int32(*flagDepth),
				Username:    *flagHTTPUsername,
				PasswordEnv: password_env,
			},
		})
	}

	return json.MarshalIndent(cfg, "", "  ")
}

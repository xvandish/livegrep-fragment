package main

// MUCH OF THE CODE IN THIS FILE IS RIPPED STRAIGHT FROM github.com/livegrep/cmd/livegrep-github-reindexer/main.go

// fro structure
// main program
//   calls program that writes the list, which calls program that then calls git fetch on repos
//   on cron
//     calls function/program that gets all repos, writes config and updates repos
//       this program should write (or return) New repos, Deleted Repos, existing repos
//     calls function/program that takes new/deleted/existing repos and delegates them to nodes with cs instances
//			first creates a directory for each pod it intends to spin up
//			root-livegrep-folder/
//			| repos/
//			| livegrep.json
//			| /pod-n
//			   | livegrep.json // the repos this pod is in charge of
//			   | repos/        // contains symlinks to the repos this pod is in charge of
//			   | i.idx         // the index that this subset of repos produces
//			then, spin up a pod with a cs instance, pointed at root-livegrep-folder/pod-n
//	   calls a function, that for every subfolder/pod that exists, calls livegrep-index to re-generate the idx
//	     then calls the cs instances Reload rpc
//   we need a loadserver as well! to balance between all of the nodes
//      the program that splits the list into nodes should write to the config, and we can load it into memory
//      Should only be about ~1.7MB, even with 4000 repos
//      Then the loadserver can lookup which node/server is responsible for that repo
//			then send and recieve request
//		In the case that a search needs to hit all repos, the loadserver needs to send fan-out request,
//			then sitch the response together and send it back

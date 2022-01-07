load("@io_bazel_rules_go//go:def.bzl", "go_binary", "go_library")
load("@bazel_gazelle//:def.bzl", "gazelle")

# gazelle:prefix github.com/xvandish/livegrep-fragment
gazelle(name = "gazelle")

go_library(
    name = "livegrep-fragment_lib",
    srcs = [
        "flags.go",
        "main.go",
    ],
    importpath = "github.com/xvandish/livegrep-fragment",
    visibility = ["//visibility:private"],
    deps = [
        "//src/proto:go_config_proto",
        "@com_github_google_go_github_v41//github:go_default_library",
        "@org_golang_x_net//context:go_default_library",
    ],
)

go_binary(
    name = "livegrep-fragment",
    embed = [":livegrep-fragment_lib"],
    visibility = ["//visibility:public"],
)

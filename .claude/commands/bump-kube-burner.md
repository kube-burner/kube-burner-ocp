Bump the kube-burner dependency to the latest available release and open a PR.

Steps:

1. Pull the latest changes from the main branch
2. Update the version of kube-burner to the latest available release, using the command `go get github.com/kube-burner/kube-burner/v2@latest`
3. Run `go mod tidy` to clean up the module files
4. Warn the user if there's any potential breaking changes in the new version.
5. Verify the project builds successfully with `make build`
6. Commit the changes with `--signoff` and the message: `Bump kube-burner to <version>`
7. Push the branch to the users's fork and open a PR with the `ok-to-test` label against `main` using `gh pr create` with:
   - Title: `Bump kube-burner to <version>`
   - Body including a link to the upstream release notes at `https://github.com/kube-burner/kube-burner/releases/tag/<version>`

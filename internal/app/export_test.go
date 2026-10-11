package app

// FragmentContextPaths returns, for the tests of the fragments, the paths of
// the elements a fragment with the given root is validated in, for one QTI
// version, in the order they are tried.
func FragmentContextPaths(v *Validator, version, root string) []string {
	var paths []string
	for _, i := range v.fragments[version].contextsFor(v.versions[version].Schema, root) {
		paths = append(paths, fragmentContexts[i].path)
	}
	return paths
}

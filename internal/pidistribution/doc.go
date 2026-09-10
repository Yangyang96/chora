// Package pidistribution selects an exact, local Pi coding-agent package.
//
// Selection is deliberately PATH-first. A private package is considered only
// when PATH is missing or rejected and the caller supplies an authenticated,
// platform-specific asset manifest. The package never downloads software,
// changes PATH, or modifies a PATH installation.
package pidistribution

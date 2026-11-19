// Package vpmify carries what the front ends show about Vpmify itself: its
// license, the notices of the third-party software in its release files, and
// where its source code is. They are embedded here because go:embed reads
// only from the directory of the package and below.
package vpmify

import _ "embed"

// SourceURL is where Vpmify's source code is published.
const SourceURL = "https://github.com/kaikoga/vpmify"

// License is Vpmify's license.
//
//go:embed LICENSE
var License string

// ThirdPartyNotices are the licenses of the third-party software in the
// release files, made by scripts/third_party_notices.sh.
//
//go:embed THIRD_PARTY_NOTICES.txt
var ThirdPartyNotices string

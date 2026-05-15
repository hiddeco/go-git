// Package packhandle owns the file-descriptor lifecycle for a pack
// triple (.pack + .idx + .rev). It composes three idxfile.SharedFile
// instances behind one PackHandle, exposing per-file refcounted
// readers via OpenPackReader / OpenIdxReader / OpenRevReader. FDs
// open lazily on first use, share across concurrent readers, and
// release after a 1-second idle grace period.
//
// PackHandle is constructed from a Sources struct that pairs three
// Source values (each carrying an opener and a size getter). The
// named-struct construction prevents pack/idx/rev swap-by-arg-order;
// the Source abstraction accommodates both on-disk files and
// in-memory-generated content (e.g. older repos without .rev files
// on disk).
package packhandle

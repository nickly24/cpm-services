package tests

// Transport envelope budget, independent of the number of questions/options.
// MongoDB still enforces its native per-document size at persistence.
const testBodyLimit = 16 << 20

# EFF dictionary regression fixture

`eff_large_wordlist.txt` is the unmodified public EFF long word list from
https://www.eff.org/files/2016/07/18/eff_large_wordlist.txt, downloaded 2026-10-15.
Its SHA-256 is checked against the production download pin in the regression test.

This fixture is used only by tests; production downloads the separate dictionary
data file. Of its 7,776 entries, four are hyphenated compounds: `drop-down`,
`felt-tip`, `t-shirt`, and `yo-yo`. The parser keeps 7,772 simple words.

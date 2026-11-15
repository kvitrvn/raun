-- The logs of the runs (epic 6, #39). Times are fixed-width UTC text, as in
-- 0002.

-- The log of a run: the text its runner sent, in numbered chunks taken in
-- order, 1 MiB at most, cut with a marker past it. next_seq is the number of
-- the chunk expected next; cut is 1 once the log was cut, after which the
-- text of later chunks is dropped and their numbers are still taken. A run
-- that sent nothing has no row. The row goes with its run: a run removed
-- because its clone credential could not be minted takes its log along,
-- and the purge deletes the logs of the runs ended 30 days ago.
CREATE TABLE run_logs (
    run      TEXT NOT NULL PRIMARY KEY REFERENCES runs (id) ON DELETE CASCADE,
    next_seq INTEGER NOT NULL CHECK (next_seq > 0),
    cut      INTEGER NOT NULL CHECK (cut IN (0, 1)),
    text     TEXT NOT NULL CHECK (length(CAST(text AS BLOB)) <= 1048576)
) STRICT;

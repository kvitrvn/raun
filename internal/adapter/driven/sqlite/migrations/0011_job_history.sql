-- The reads of the screens of jobs and runners (epic 6, #41).

-- The jobs of a repository in every state, newest first: the Runs list.
-- jobs_repository of 0008 holds the queued and running ones only.
CREATE INDEX jobs_history ON jobs (forge, repository, created_at DESC, id DESC);

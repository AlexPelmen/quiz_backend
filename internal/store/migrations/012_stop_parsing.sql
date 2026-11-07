ALTER TABLE parse_runs DROP CONSTRAINT parse_runs_status_check;
ALTER TABLE parse_runs ADD CONSTRAINT parse_runs_status_check CHECK (status IN ('pending','running','stopping','stopped','finished','errored'));
DROP INDEX one_active_run;
CREATE UNIQUE INDEX one_active_run ON parse_runs((true)) WHERE status IN ('pending','running','stopping');

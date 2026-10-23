-- "unverified": a solution was worked out, but its success is not confirmed.
UPDATE kinds SET statuses = ARRAY['open', 'investigating', 'unverified', 'solved', 'workaround', 'wontfix']
 WHERE key = 'problem' AND NOT ('unverified' = ANY(statuses));

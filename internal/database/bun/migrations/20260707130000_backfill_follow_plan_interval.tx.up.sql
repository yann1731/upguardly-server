-- Backfill: convert monitors that are sitting exactly at their owner's current
-- plan floor to follow-plan (NULL). Every monitor created before this feature
-- got the plan floor materialized into its interval (the create handler always
-- did that, and the UI never offered an interval field), so this converts the
-- whole existing fleet to follow-plan without changing anyone's effective
-- cadence.
--
-- ORDERING: run this ONLY after every scheduler binary understands a NULL
-- interval. An old binary scans a NULL interval into a Go int and would stop
-- scheduling that monitor (runner.go bails on interval <= 0). The schema
-- migration (20260707120000) is safe to deploy with the new binaries; this
-- backfill is the final step once the rollout is complete.
--
-- A monitor whose owner deliberately set interval equal to their plan floor via
-- the API is indistinguishable here and also becomes follow-plan; behavior is
-- identical until the owner's next plan change, so this is acceptable.

UPDATE monitors m
   SET interval = NULL
  FROM (
        SELECT m2.id,
               CASE COALESCE(
                        CASE WHEN s.status = 'CANCELED' THEN 'FREE' ELSE s.plan::text END,
                        'FREE')
                    WHEN 'FREE' THEN 300
                    ELSE 60
               END AS floor
          FROM monitors m2
          LEFT JOIN organizations o ON o.id = m2.org_id
          LEFT JOIN subscriptions s ON s.user_id = COALESCE(o.owner_id, m2.user_id)
       ) x
 WHERE x.id = m.id
   AND m.interval = x.floor;

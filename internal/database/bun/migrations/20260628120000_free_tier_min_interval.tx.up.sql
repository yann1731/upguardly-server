-- Bump existing FREE-tier monitors up to the new 5-minute (300s) minimum check
-- interval. Free-tier intervals were previously gated only by the global 60s
-- floor; the per-plan minimum (see models.LimitsForPlan) now raises FREE to 300s
-- at the API layer, so this backfills monitors created before that change.
--
-- A monitor is FREE-tier when it is solo (org_id IS NULL) and its owner has no
-- subscription row or one on the FREE plan. Org monitors are owned by an
-- ENTERPRISE account (only ENTERPRISE can create orgs) and are left untouched.
UPDATE "monitors" m
   SET "interval" = 300,
       "updated_at" = NOW()
 WHERE m."interval" < 300
   AND m."org_id" IS NULL
   AND NOT EXISTS (
         SELECT 1
           FROM "subscriptions" s
          WHERE s."user_id" = m."user_id"
            AND s."plan" <> 'FREE'
       );

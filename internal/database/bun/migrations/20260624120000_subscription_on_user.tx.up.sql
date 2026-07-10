-- Move subscriptions from organizations to users (the account owner).
-- FREE/PRO are single-user plans; ENTERPRISE unlocks orgs/seats. The billing
-- subject is the user, so the subscription now keys on user_id.

-- Drop the org foreign key and its unique index.
ALTER TABLE "subscriptions" DROP CONSTRAINT IF EXISTS "subscriptions_organization_id_fkey";
DROP INDEX IF EXISTS "subscriptions_organization_id_key";

-- Add the new column and backfill from each org's owner.
ALTER TABLE "subscriptions" ADD COLUMN "user_id" TEXT;
UPDATE "subscriptions" s
   SET "user_id" = o."owner_id"
  FROM "organizations" o
 WHERE s."organization_id" = o."id";

-- Any subscription that couldn't be mapped to an owner is orphaned; drop it.
DELETE FROM "subscriptions" WHERE "user_id" IS NULL;

ALTER TABLE "subscriptions" ALTER COLUMN "user_id" SET NOT NULL;
ALTER TABLE "subscriptions" DROP COLUMN "organization_id";
CREATE UNIQUE INDEX "subscriptions_user_id_key" ON "subscriptions"("user_id");

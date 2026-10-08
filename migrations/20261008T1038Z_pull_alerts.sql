-- A monitoring notice exists even when its Subscription has no destination.
-- Keep the Delivery foreign key for notices that do have a delivery.
ALTER TABLE monitoring_notices ALTER COLUMN delivery_id DROP NOT NULL;

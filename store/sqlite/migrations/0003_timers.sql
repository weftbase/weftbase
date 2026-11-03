-- Phase 1 timers: a run remembers its queue item (the queue
-- time-to-live completes it), when it parked, and a parked run past its
-- maximum pin age records where it was migrated (its resume then takes
-- that version, resolved_via migrate) and when.
ALTER TABLE runs ADD COLUMN queue_item TEXT NOT NULL DEFAULT '';
ALTER TABLE runs ADD COLUMN parked_at TEXT;
ALTER TABLE runs ADD COLUMN migrated_to TEXT NOT NULL DEFAULT '';
ALTER TABLE runs ADD COLUMN pin_expired_at TEXT;

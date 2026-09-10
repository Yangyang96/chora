-- Preserve review IDs, decisions and comment text while aligning terminology.
ALTER TABLE review_decisions RENAME COLUMN reviewer_note TO comment;

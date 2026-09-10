-- Project context is independent of its General Room brief.
ALTER TABLE projects ADD COLUMN description TEXT NOT NULL DEFAULT '' CHECK(length(description)<=16384);

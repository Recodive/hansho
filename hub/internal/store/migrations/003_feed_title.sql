-- A sender can give a feed a human name; the app lists a person's feeds by it instead of asking for the id.
ALTER TABLE feeds ADD COLUMN title TEXT;

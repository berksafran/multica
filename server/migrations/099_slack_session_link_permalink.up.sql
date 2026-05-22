-- Slack thread permalink cached at link-creation time so the UI can render a
-- clickable "open in Slack" affordance without a per-request chat.getPermalink
-- round-trip. Nullable: if the API call fails at create time the column stays
-- empty and the UI degrades to plain text.
ALTER TABLE slack_chat_session_link
    ADD COLUMN permalink TEXT;

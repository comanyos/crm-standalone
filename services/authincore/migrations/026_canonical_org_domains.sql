-- Canonicalise stored organization domains to match request-Host resolution.
--
-- Tenant resolution lowercases the Host, strips the port, drops IPv6 brackets
-- and trims the root dot, then compares for exact equality against
-- orgs.primary_domain. Rows written before that canonicalisation existed still
-- carry ports ("localhost:8080"), so no request could resolve them and every
-- tenant 404'd. CreateOrg now canonicalises on write; this brings existing rows
-- into line.
--
-- Only unambiguously safe values are rewritten. Anything that looks like a URL
-- rather than an authority is left exactly as it is: guessing which part was
-- meant could silently repoint a tenant at a domain someone else controls,
-- which is worse than leaving a row an operator has to look at.
--
-- +goose Up

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION authincore_canonical_domain(raw TEXT)
RETURNS TEXT AS $$
DECLARE
    host TEXT;
BEGIN
    IF raw IS NULL OR btrim(raw) = '' THEN
        RETURN NULL;
    END IF;
    host := btrim(raw);

    -- Refuse URL-shaped input: scheme, path, query, fragment, credentials or
    -- embedded whitespace. These are the cases the Go validator rejects too.
    IF host ~ '(://|/|\?|#|@|\s)' THEN
        RETURN NULL;
    END IF;

    -- Bracketed IPv6, optionally with a port: [::1] or [::1]:8080.
    IF host ~ '^\[' THEN
        host := substring(host from '^\[(.*)\]');
        IF host IS NULL THEN
            RETURN NULL;
        END IF;
        RETURN lower(host);
    END IF;

    -- A bare IPv6 literal has many colons and no port to strip; only strip a
    -- port when there is exactly one colon, so "::1" survives intact.
    IF (length(host) - length(replace(host, ':', ''))) = 1 THEN
        host := split_part(host, ':', 1);
    END IF;

    host := lower(rtrim(host, '.'));
    IF host = '' THEN
        RETURN NULL;
    END IF;
    RETURN host;
END;
$$ LANGUAGE plpgsql IMMUTABLE;
-- +goose StatementEnd

-- +goose StatementBegin
DO $$
DECLARE
    o        RECORD;
    proposed TEXT;
    clash    UUID;
BEGIN
    FOR o IN SELECT id, instance_id, primary_domain FROM orgs LOOP
        proposed := authincore_canonical_domain(o.primary_domain);

        IF proposed IS NULL THEN
            RAISE WARNING 'org % has a domain that cannot be canonicalised (%); leaving it unchanged',
                o.id, o.primary_domain;
            CONTINUE;
        END IF;

        CONTINUE WHEN proposed = o.primary_domain;

        -- UNIQUE (instance_id, primary_domain): two rows can canonicalise onto
        -- the same value ("ACME.test" and "acme.test:8080"). Updating blindly
        -- would abort the whole migration on a constraint violation; skipping
        -- with a warning leaves both rows readable and the choice with an
        -- operator who knows which one is real.
        SELECT id INTO clash FROM orgs
         WHERE instance_id = o.instance_id AND primary_domain = proposed AND id <> o.id
         LIMIT 1;

        IF clash IS NOT NULL THEN
            RAISE WARNING 'org % (%) would collide with org % on canonical domain %; leaving it unchanged',
                o.id, o.primary_domain, clash, proposed;
            CONTINUE;
        END IF;

        UPDATE orgs SET primary_domain = proposed, updated_at = NOW() WHERE id = o.id;
        RAISE NOTICE 'org %: % -> %', o.id, o.primary_domain, proposed;
    END LOOP;
END;
$$;
-- +goose StatementEnd

-- +goose Down

-- Canonicalisation discards the original text (the port, the letter case), so
-- there is nothing to restore. The helper is dropped; the rewritten values
-- stand, because they are the ones that actually resolve.
DROP FUNCTION IF EXISTS authincore_canonical_domain(TEXT);

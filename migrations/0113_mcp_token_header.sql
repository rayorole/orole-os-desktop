-- Header that carries a bearer-kind server's static token: '' sends "Authorization: Bearer <token>",
-- a name such as 'api-key' sends "<name>: <token>" instead (Similarweb takes API keys only that way).
-- Fixed when the server is added; the token itself stays in the credential store.
ALTER TABLE mcp_servers ADD COLUMN token_header TEXT NOT NULL DEFAULT '';

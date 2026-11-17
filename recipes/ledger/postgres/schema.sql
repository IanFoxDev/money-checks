-- The tables of ianfoxdev/ledger 0.2, copied from its schema/postgresql.sql.
-- Rows in these tables are only inserted, never updated or deleted.
-- Use READ COMMITTED (the PostgreSQL default) for transactions that write to the ledger.

CREATE TABLE ledger_accounts (
    code           varchar(190) PRIMARY KEY,
    type           varchar(16)  NOT NULL CHECK (type IN ('asset', 'liability', 'equity', 'revenue', 'expense')),
    currency       varchar(12)  NOT NULL,
    allow_negative boolean      NOT NULL,
    created_at     timestamp(6) NOT NULL DEFAULT (now() AT TIME ZONE 'UTC')
);

CREATE TABLE ledger_transactions (
    id              bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    idempotency_key varchar(190) NOT NULL UNIQUE,
    hash            char(64)     NOT NULL,
    type            varchar(64)  NOT NULL,
    meta            text         NOT NULL, -- canonical JSON
    reverses        bigint       NULL UNIQUE REFERENCES ledger_transactions (id),
    created_at      timestamp(6) NOT NULL -- UTC
);

-- The primary key (account, sequence) keeps each account's chain linear: a write that
-- slipped past the account lock fails instead of forking it.
CREATE TABLE ledger_postings (
    account        varchar(190)   NOT NULL REFERENCES ledger_accounts (code),
    sequence       bigint         NOT NULL CHECK (sequence > 0),
    transaction_id bigint         NOT NULL REFERENCES ledger_transactions (id),
    position       smallint       NOT NULL,
    side           char(1)        NOT NULL CHECK (side IN ('D', 'C')),
    amount         numeric(38, 0) NOT NULL CHECK (amount > 0),
    balance_after  numeric(38, 0) NOT NULL,
    PRIMARY KEY (account, sequence)
);

CREATE INDEX ledger_postings_transaction ON ledger_postings (transaction_id);

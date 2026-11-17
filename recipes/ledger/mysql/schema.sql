-- The tables of ianfoxdev/ledger 0.2, copied from its schema/mysql.sql.
-- Rows in these tables are only inserted, never updated or deleted.
-- Account codes are ASCII with a binary collation: case-sensitive, and sorted the way
-- PHP sorts them, which is the order the ledger locks them in.

CREATE TABLE ledger_accounts (
    code           varchar(190) CHARACTER SET ascii COLLATE ascii_bin NOT NULL PRIMARY KEY,
    type           varchar(16)  NOT NULL,
    currency       varchar(12)  CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    allow_negative tinyint(1)   NOT NULL,
    created_at     datetime(6)  NOT NULL DEFAULT (UTC_TIMESTAMP(6)),
    CHECK (type IN ('asset', 'liability', 'equity', 'revenue', 'expense'))
) ENGINE = InnoDB;

CREATE TABLE ledger_transactions (
    id              bigint       NOT NULL AUTO_INCREMENT PRIMARY KEY,
    idempotency_key varchar(190) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
    hash            char(64)     CHARACTER SET ascii NOT NULL,
    type            varchar(64)  NOT NULL,
    meta            longtext     CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL, -- canonical JSON
    reverses        bigint       NULL,
    created_at      datetime(6)  NOT NULL, -- UTC
    UNIQUE KEY ledger_transactions_key (idempotency_key),
    UNIQUE KEY ledger_transactions_reverses (reverses),
    FOREIGN KEY (reverses) REFERENCES ledger_transactions (id)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4;

-- The primary key (account, sequence) keeps each account's chain linear: a write that
-- slipped past the account lock fails instead of forking it.
CREATE TABLE ledger_postings (
    account        varchar(190)   CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    sequence       bigint         NOT NULL,
    transaction_id bigint         NOT NULL,
    position       smallint       NOT NULL,
    side           char(1)        CHARACTER SET ascii NOT NULL,
    amount         decimal(38, 0) NOT NULL,
    balance_after  decimal(38, 0) NOT NULL,
    PRIMARY KEY (account, sequence),
    KEY ledger_postings_transaction (transaction_id),
    FOREIGN KEY (account) REFERENCES ledger_accounts (code),
    FOREIGN KEY (transaction_id) REFERENCES ledger_transactions (id),
    CHECK (sequence > 0),
    CHECK (side IN ('D', 'C')),
    CHECK (amount > 0)
) ENGINE = InnoDB;

-- The tables the PostgreSQL recipes read. Amounts are numeric in major units,
-- as most Laravel and Symfony apps keep them; minor units in bigint work the same
-- with unit: minor in the check.

-- An account per owner and currency. The application keeps the balance.
create table accounts (
    id             text primary key,
    owner          text           not null,
    currency       char(3)        not null,
    balance        numeric(20, 2) not null,
    -- House and credit accounts may go below zero, customer ones may not.
    allow_negative boolean        not null default false
);

-- Double-entry: each transaction moves money between accounts, and its entries
-- sum to zero in each currency. Credits are positive, debits negative.
create table entries (
    id         bigserial      primary key,
    txn_id     text           not null,
    account_id text           not null references accounts,
    amount     numeric(20, 2) not null,
    created_at timestamptz    not null
);
create index on entries (txn_id);
create index on entries (account_id);
create index on entries (created_at);

-- Orders, the payments a provider (PSP) made for them, and refunds of payments.
-- provider_txn_id is the provider's id of the payment, unique per provider only.
create table orders (
    id         text           primary key,
    owner      text           not null,
    amount     numeric(20, 2) not null,
    currency   char(3)        not null,
    created_at timestamptz    not null,
    -- When the customer got what they paid for; null until then.
    fulfilled_at timestamptz
);
create table payments (
    id              text           primary key,
    order_id        text           not null references orders,
    provider        text           not null,
    provider_txn_id text           not null,
    status          text           not null,  -- pending, succeeded, failed
    amount          numeric(20, 2) not null,
    currency        char(3)        not null,
    created_at      timestamptz    not null
);
create index on payments (provider, provider_txn_id);
create index on payments (created_at);
-- Pending payments are few; the index keeps finding them cheap.
create index on payments (created_at) where status = 'pending';
create table refunds (
    id              text           primary key,
    payment_id      text           not null references payments,
    amount          numeric(20, 2) not null,
    -- The provider's id of the charge it refunded, as its webhook named it;
    -- null for refunds made in the app.
    provider_txn_id text,
    created_at      timestamptz    not null
);
create index on refunds (payment_id);
create index on refunds (created_at);
-- Chargebacks the provider opened on payments. A chargeback the merchant won is
-- reversed: the money comes back and the status is 'won'.
create table chargebacks (
    id         text           primary key,
    payment_id text           not null references payments,
    amount     numeric(20, 2) not null,
    status     text           not null,  -- open, lost, won
    created_at timestamptz    not null
);
create index on chargebacks (payment_id);
create index on chargebacks (created_at);

-- Subscriptions: access is paid for until current_period_end.
create table subscriptions (
    id                 text           primary key,
    owner              text           not null,
    status             text           not null,  -- active, cancelled, expired
    price              numeric(20, 2) not null,
    currency           char(3)        not null,
    current_period_end timestamptz    not null
);
create index on subscriptions (current_period_end) where status = 'active';

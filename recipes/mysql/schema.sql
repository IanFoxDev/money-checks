-- The tables the MySQL recipes read, the same as in postgres/schema.sql. Amounts
-- are decimal in major units and times are timestamp, as Laravel's decimal() and
-- timestamps() create them; minor units in bigint work the same with unit: minor
-- in the check.

-- An account per owner and currency. The application keeps the balance.
create table accounts (
    id             varchar(64)    primary key,
    owner          varchar(64)    not null,
    currency       char(3)        not null,
    balance        decimal(20, 2) not null,
    -- House and credit accounts may go below zero, customer ones may not.
    allow_negative boolean        not null default false
);

-- Double-entry: each transaction moves money between accounts, and its entries
-- sum to zero in each currency. Credits are positive, debits negative.
create table entries (
    id         bigint unsigned auto_increment primary key,
    txn_id     varchar(64)     not null,
    account_id varchar(64)     not null,
    amount     decimal(20, 2)  not null,
    created_at timestamp       not null,
    foreign key (account_id) references accounts (id),
    index (txn_id),
    index (created_at)
);

-- Orders, the payments a provider (PSP) made for them, and refunds of payments.
-- provider_txn_id is the provider's id of the payment, unique per provider only.
create table orders (
    id         varchar(64)    primary key,
    owner      varchar(64)    not null,
    amount     decimal(20, 2) not null,
    currency   char(3)        not null,
    created_at timestamp      not null,
    -- When the customer got what they paid for; null until then.
    fulfilled_at timestamp    null
);
create table payments (
    id              varchar(64)    primary key,
    order_id        varchar(64)    not null,
    provider        varchar(32)    not null,
    provider_txn_id varchar(128)   not null,
    status          varchar(16)    not null,  -- pending, succeeded, failed
    amount          decimal(20, 2) not null,
    currency        char(3)        not null,
    created_at      timestamp      not null,
    foreign key (order_id) references orders (id),
    index (provider, provider_txn_id),
    index (created_at),
    -- MySQL has no partial index; this one keeps finding pending payments cheap.
    index (status, created_at)
);
create table refunds (
    id              varchar(64)    primary key,
    payment_id      varchar(64)    not null,
    amount          decimal(20, 2) not null,
    -- The provider's id of the charge it refunded, as its webhook named it;
    -- null for refunds made in the app.
    provider_txn_id varchar(128),
    created_at      timestamp      not null,
    foreign key (payment_id) references payments (id),
    index (created_at)
);
-- Chargebacks the provider opened on payments. A chargeback the merchant won is
-- reversed: the money comes back and the status is 'won'.
create table chargebacks (
    id         varchar(64)    primary key,
    payment_id varchar(64)    not null,
    amount     decimal(20, 2) not null,
    status     varchar(16)    not null,  -- open, lost, won
    created_at timestamp      not null,
    foreign key (payment_id) references payments (id),
    index (created_at)
);

-- Subscriptions: access is paid for until current_period_end.
create table subscriptions (
    id                 varchar(64)    primary key,
    owner              varchar(64)    not null,
    status             varchar(16)    not null,  -- active, cancelled, expired
    price              decimal(20, 2) not null,
    currency           char(3)        not null,
    current_period_end timestamp      not null,
    index (status, current_period_end)
);

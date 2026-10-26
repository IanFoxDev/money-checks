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

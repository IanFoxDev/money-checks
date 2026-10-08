-- A double-entry ledger: every transaction's entries sum to zero per currency.
-- Two transactions are planted that do not, and one wallet goes below zero.
create table ledger_entries (
    id             bigserial primary key,
    transaction_id text   not null,
    account        text   not null,
    amount_minor   bigint not null,
    currency       char(3) not null
);

insert into ledger_entries (transaction_id, account, amount_minor, currency) values
    ('t1', 'cash',          -2000, 'USD'),
    ('t1', 'wallet:anna',    2000, 'USD'),
    ('t2', 'revenue',        -150, 'USD'),  -- a fee booked on one side only
    ('t2', 'wallet:ben',      100, 'USD'),
    ('t3', 'cash',          -1200, 'JPY'),
    ('t3', 'wallet:emil',    1200, 'JPY'),
    ('t4', 'cash',           -500, 'EUR'),
    ('t4', 'wallet:carl',     500, 'EUR'),
    ('t5', 'cash',          -1000, 'EUR'),  -- a conversion with the FX leg missing
    ('t5', 'wallet:dana',    1000, 'USD'),
    ('t6', 'wallet:carl',    -300, 'USD'),  -- balanced, but takes the wallet below zero
    ('t6', 'revenue',         300, 'USD');

-- Rows that break each recipe, and rows next to them that must not.

-- An account per owner and currency for every entry below. The stored balance is
-- the sum of the account's entries, except hugo (50.00, entries 40.00) and judy
-- (15.00, no entries at all): balance_matches_entries. ivan has no entries and 0.
-- negative_balance: ben and dana are below zero; carl is at zero, and the house,
-- cash and FX accounts are allowed to go negative.
insert into accounts (id, owner, currency, balance, allow_negative) values
    ('acc_anna',       'anna',   'USD',    25.00, false),
    ('acc_ben',        'ben',    'EUR',    -3.50, false),
    ('acc_carl',       'carl',   'USD',     0.00, false),
    ('acc_dana',       'dana',   'JPY',  -500.00, false),
    ('acc_eve',        'eve',    'USD',   118.00, false),
    ('acc_frank',      'frank',  'USD',    11.00, false),
    ('acc_gina',       'gina',   'EUR',     0.00, false),
    ('acc_gina_usd',   'gina',   'USD',    10.00, false),
    ('acc_hugo',       'hugo',   'USD',    50.00, false),
    ('acc_ivan',       'ivan',   'USD',     0.00, false),
    ('acc_judy',       'judy',   'EUR',    15.00, false),
    ('acc_house',      'house',  'USD',  -120.00, true),
    ('cash_usd',       'house',  'USD',   -70.00, true),
    ('cash_eur',       'house',  'EUR',   -29.00, true),
    ('cash_jpy',       'house',  'JPY', -1000.00, true),
    ('revenue_usd',    'house',  'USD',     6.50, true),
    ('revenue_eur',    'house',  'EUR',    13.50, true),
    ('revenue_jpy',    'house',  'JPY',  1500.00, true),
    ('fx_usd',         'house',  'USD',   -10.00, true),
    ('fx_eur',         'house',  'EUR',     9.00, true);

-- Double-entry: every transaction sums to zero in each currency. t11 and t12 do
-- not (unbalanced_transaction); t14 has two currencies and balances in each.
insert into entries (txn_id, account_id, amount, created_at) values
    -- anna tops up
    ('t01', 'cash_usd',        -30.00, '2026-10-01T08:00:00Z'),
    ('t01', 'acc_anna',         30.00, '2026-10-01T08:00:00Z'),
    -- anna buys
    ('t02', 'acc_anna',         -5.00, '2026-10-01T08:30:00Z'),
    ('t02', 'revenue_usd',       5.00, '2026-10-01T08:30:00Z'),
    -- ben tops up
    ('t03', 'cash_eur',        -10.00, '2026-10-01T09:00:00Z'),
    ('t03', 'acc_ben',          10.00, '2026-10-01T09:00:00Z'),
    -- ben buys twice at once: both purchases read 10.00
    ('t04', 'acc_ben',          -6.75, '2026-10-01T09:30:00Z'),
    ('t04', 'revenue_eur',       6.75, '2026-10-01T09:30:00Z'),
    ('t05', 'acc_ben',          -6.75, '2026-10-01T10:00:00Z'),
    ('t05', 'revenue_eur',       6.75, '2026-10-01T10:00:00Z'),
    -- carl tops up and withdraws all of it
    ('t06', 'cash_usd',        -12.00, '2026-10-01T10:30:00Z'),
    ('t06', 'acc_carl',         12.00, '2026-10-01T10:30:00Z'),
    ('t07', 'acc_carl',        -12.00, '2026-10-01T11:00:00Z'),
    ('t07', 'cash_usd',         12.00, '2026-10-01T11:00:00Z'),
    -- dana tops up and buys for more than the balance
    ('t08', 'cash_jpy',         -1000, '2026-10-01T11:30:00Z'),
    ('t08', 'acc_dana',          1000, '2026-10-01T11:30:00Z'),
    ('t09', 'acc_dana',         -1500, '2026-10-01T12:00:00Z'),
    ('t09', 'revenue_jpy',       1500, '2026-10-01T12:00:00Z'),
    -- a payout to eve from the house account
    ('t10', 'acc_house',      -120.00, '2026-10-01T12:30:00Z'),
    ('t10', 'acc_eve',         120.00, '2026-10-01T12:30:00Z'),
    -- unbalanced: the fee is booked as 1.50 on the other side
    ('t11', 'acc_eve',          -2.00, '2026-10-01T13:00:00Z'),
    ('t11', 'revenue_usd',       1.50, '2026-10-01T13:00:00Z'),
    -- unbalanced: a conversion with the FX legs missing
    ('t12', 'cash_eur',        -10.00, '2026-10-01T13:30:00Z'),
    ('t12', 'acc_frank',        11.00, '2026-10-01T13:30:00Z'),
    -- gina tops up in EUR
    ('t13', 'cash_eur',         -9.00, '2026-10-01T14:00:00Z'),
    ('t13', 'acc_gina',          9.00, '2026-10-01T14:00:00Z'),
    -- balanced: gina converts EUR to USD, zero in each currency
    ('t14', 'acc_gina',         -9.00, '2026-10-01T14:30:00Z'),
    ('t14', 'fx_eur',            9.00, '2026-10-01T14:30:00Z'),
    ('t14', 'fx_usd',          -10.00, '2026-10-01T14:30:00Z'),
    ('t14', 'acc_gina_usd',     10.00, '2026-10-01T14:30:00Z'),
    -- hugo tops up 40.00, the stored balance says 50.00 (balance_matches_entries)
    ('t15', 'cash_usd',        -40.00, '2026-10-01T15:30:00Z'),
    ('t15', 'acc_hugo',         40.00, '2026-10-01T15:30:00Z');

-- Orders, payments and refunds.
insert into orders (id, owner, amount, currency, created_at) values
    ('o1', 'anna',     20.00, 'USD', '2026-10-02T09:00:00Z'),
    ('o2', 'ben',      15.00, 'EUR', '2026-10-02T09:00:00Z'),
    ('o3', 'carl',      3000, 'JPY', '2026-10-02T09:00:00Z'),
    ('o4', 'dana',     50.00, 'USD', '2026-10-02T09:00:00Z'),
    ('o5', 'eve',      10.00, 'USD', '2026-10-02T09:00:00Z'),
    ('o6', 'frank',    40.00, 'EUR', '2026-10-02T09:00:00Z'),
    ('o7', 'gina',     25.00, 'USD', '2026-10-02T09:00:00Z');

insert into payments (id, order_id, provider, provider_txn_id, status, amount, currency, created_at) values
    -- duplicate_provider_txn: one acmepay payment, its webhook handled twice
    ('p1', 'o1', 'acmepay', 'A-1001', 'succeeded',  20.00, 'USD', '2026-10-02T09:00:00Z'),
    ('p2', 'o1', 'acmepay', 'A-1001', 'succeeded',  20.00, 'USD', '2026-10-02T09:01:00Z'),
    -- the same provider id at another provider is another payment
    ('p3', 'o2', 'acmepay', 'A-1002', 'succeeded',  15.00, 'EUR', '2026-10-02T09:10:00Z'),
    ('p4', 'o3', 'zenpay',  'A-1002', 'succeeded',   3000, 'JPY', '2026-10-02T09:20:00Z'),
    ('p5', 'o4', 'zenpay',  'Z-77',   'succeeded',  50.00, 'USD', '2026-10-02T09:30:00Z'),
    -- currency_mismatch: the order is in USD; the failed attempt does not count, p7 does
    ('p6', 'o5', 'acmepay', 'A-1003', 'failed',     10.00, 'EUR', '2026-10-02T09:40:00Z'),
    ('p7', 'o5', 'acmepay', 'A-1004', 'succeeded',  10.00, 'EUR', '2026-10-02T09:41:00Z'),
    ('p8', 'o6', 'zenpay',  'Z-78',   'succeeded',  40.00, 'EUR', '2026-10-02T09:50:00Z'),
    ('p9', 'o7', 'acmepay', 'A-1005', 'succeeded',  25.00, 'USD', '2026-10-02T10:00:00Z');

insert into refunds (id, payment_id, amount, created_at) values
    -- a full refund
    ('r1', 'p3',  15.00, '2026-10-02T10:40:00Z'),
    -- two partial refunds that add up to the payment
    ('r2', 'p5',  20.00, '2026-10-02T10:50:00Z'),
    ('r3', 'p5',  30.00, '2026-10-02T11:00:00Z'),
    -- refund_exceeds_payment: retried after a timeout, the first one went through
    ('r4', 'p8',  40.00, '2026-10-02T11:10:00Z'),
    ('r5', 'p8',  40.00, '2026-10-02T11:11:00Z'),
    -- refund_exceeds_payment: a manual refund on top of the automatic one, 5.00 over
    ('r6', 'p9',  10.00, '2026-10-02T11:20:00Z'),
    ('r7', 'p9',  20.00, '2026-10-02T11:30:00Z');

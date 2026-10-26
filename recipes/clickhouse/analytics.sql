-- Payment events as a Kafka consumer writes them into ClickHouse for dashboards
-- and partner reports: one row per successful payment or refund. Plain MergeTree;
-- ReplacingMergeTree removes duplicates only when parts merge, which may be never
-- for rows in different partitions, and a query without FINAL counts them all.
create table payments
(
    payment_id String,
    kind       LowCardinality(String),  -- payment, refund
    provider   LowCardinality(String),
    amount     Decimal(18, 2),
    currency   LowCardinality(String),
    created_at DateTime64(3, 'UTC')
)
engine = MergeTree
partition by toYYYYMM(created_at)
order by (created_at, payment_id);

-- The successful payments of the app, against which daily_totals_match compares:
--   p9 (25.00 USD) never arrived: its batch was lost;
--   p3 (15.00 EUR) arrived twice, see below;
--   t-1 (500 JPY) is a test payment written to analytics only.
-- The refund of p5 shares the payment's id and is not a duplicate. (ClickHouse
-- does not take comments inside VALUES.)
insert into payments values
    ('p1',  'payment', 'acmepay',  20.00, 'USD', '2026-10-02 09:00:00'),
    ('p2',  'payment', 'acmepay',  20.00, 'USD', '2026-10-02 09:01:00'),
    ('p3',  'payment', 'acmepay',  15.00, 'EUR', '2026-10-02 09:10:00'),
    ('p4',  'payment', 'zenpay',    3000, 'JPY', '2026-10-02 09:20:00'),
    ('p5',  'payment', 'zenpay',   50.00, 'USD', '2026-10-02 09:30:00'),
    ('p7',  'payment', 'acmepay',  10.00, 'EUR', '2026-10-02 09:41:00'),
    ('p8',  'payment', 'zenpay',   40.00, 'EUR', '2026-10-02 09:50:00'),
    ('t-1', 'payment', 'acmepay',    500, 'JPY', '2026-10-02 12:00:00'),
    ('p5',  'refund',  'zenpay',   20.00, 'USD', '2026-10-02 10:50:00');

-- duplicate_rows: the consumer crashed after the insert and before it committed
-- its offset, so the batch with p3 came again.
insert into payments values
    ('p3',  'payment', 'acmepay',  15.00, 'EUR', '2026-10-02 09:10:00');

-- Dated from now, as in the app's seed.
insert into payments values
    ('p13', 'payment', 'zenpay',   12.00, 'EUR', now64(3) - interval 2 hour),
    ('p14', 'payment', 'acmepay',   7.00, 'USD', now64(3) - interval 5 minute),
    ('p15', 'payment', 'zenpay',   20.00, 'USD', now64(3) - interval 2 hour),
    ('p15', 'refund',  'zenpay',   20.00, 'USD', now64(3) - interval 1 hour);

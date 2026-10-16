-- Daily payment totals, as a Kafka consumer of the shop's payment events writes
-- them for dashboards and partner payouts. One row per batch, so a day can have
-- several rows. Planted against the payments in MongoDB:
--   2026-09-01 USD: one of the two renewals never arrived (9.99 and 1 payment short)
--   2026-09-05 USD: the batch with the 99.00 payment was lost
--   2026-09-08 USD: a test payment written to statistics only
create table daily_payments
(
    day      Date,
    currency LowCardinality(String),
    amount   Decimal(18, 2),
    payments UInt32
)
engine = MergeTree
order by (day, currency);

insert into daily_payments values
    ('2026-08-01', 'USD', 9.99, 1),
    ('2026-09-01', 'USD', 9.99, 1),
    ('2026-09-02', 'EUR', 19.99, 1),
    ('2026-09-07', 'JPY', 1200, 1),
    ('2026-09-07', 'JPY', 1200, 1),
    ('2026-09-08', 'USD', 5.00, 1);

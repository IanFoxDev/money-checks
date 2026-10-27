-- Payments whose money went back twice: refunded by support and then charged
-- back by the customer's bank, or charged back twice because the provider's
-- reversal of a chargeback was written as a new one. The bank takes the
-- chargeback whatever the refund did. A chargeback the merchant won costs
-- nothing and does not count; payments refunded with no chargeback are left to
-- refund_exceeds_payment. A provider that reports chargebacks in its settlement
-- currency can be off by cents of exchange: compare with a tolerance then.
with touched as (
    select payment_id from chargebacks where created_at >= $1
    union
    select payment_id from refunds where created_at >= $1
), returned as (
    select p.id, p.currency, p.amount,
           (select coalesce(sum(r.amount), 0) from refunds r where r.payment_id = p.id) as refunded,
           (select coalesce(sum(c.amount), 0) from chargebacks c
             where c.payment_id = p.id and c.status <> 'won') as charged_back,
           (select count(*) from chargebacks c
             where c.payment_id = p.id and c.status <> 'won') as chargebacks
    from payments p
    where p.id in (select payment_id from touched)
)
select id, currency, amount, refunded, charged_back, chargebacks,
       refunded + charged_back - amount as over
from returned
where chargebacks > 0 and refunded + charged_back > amount
order by id

-- Successful payments in another currency than their order: a checkout that
-- sent the default currency to the provider, or a price list switched while the
-- cart was open. 10.00 EUR paid for a 10.00 USD order passes every check that
-- compares amounts only.
select p.id, p.currency, p.amount,
       o.id as order_id, o.amount as order_amount, o.currency as order_currency
from payments p
join orders o on o.id = p.order_id
where p.status = 'succeeded' and p.currency <> o.currency and p.created_at >= $1
order by p.id

select payment_id, sum(amount_minor) as amount, currency
from refunds
group by payment_id, currency
having count(*) > 1

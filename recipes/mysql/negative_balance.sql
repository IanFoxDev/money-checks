-- Accounts below zero that may not be. Usually two debits that both read the
-- old balance: a withdrawal racing a purchase, or a job retried after a timeout.
select id, owner, currency, balance
from accounts
where balance < 0 and not allow_negative
order by id

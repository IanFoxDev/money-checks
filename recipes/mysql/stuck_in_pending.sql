-- Payments still pending an hour after they started: the provider's webhook was
-- lost, the worker that polls the provider died, or the status update failed
-- after the provider answered. The customer may have paid; nobody knows yet.
-- Set the hour to a little more than your provider's longest normal wait.
-- No window: a payment stuck for a week is the one that matters most, and a
-- window would have let it go.
select id, provider, provider_txn_id, amount, currency, created_at
from payments
where status = 'pending' and created_at < now() - interval 1 hour
order by created_at

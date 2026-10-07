-- +goose Up
-- Indexes for the queries the baseline left to sequential scans.

-- Open invoices of a Telegram user: CountOpenPayments, FindOpenPayment, FindOpenPackagePayment.
CREATE INDEX payments_tg ON payments(tg_id, status, created_at);

-- Autotune reads and prunes by time; deleting an inbound cascades by inbound_id.
CREATE INDEX inbound_reach_at ON inbound_reach(at);
CREATE INDEX inbound_reach_inbound ON inbound_reach(inbound_id);
CREATE INDEX sub_fetches_fetched ON sub_fetches(fetched_at);
CREATE INDEX inbound_events_created ON inbound_events(created_at);

-- Telegram notices are pruned by time.
CREATE INDEX tg_notices_sent ON tg_notices(sent_at);

-- Deleting a pool cascades by pool_id; the primary keys lead with the other column.
CREATE INDEX user_pools_pool ON user_pools(pool_id);
CREATE INDEX tariff_pools_pool ON tariff_pools(pool_id);

-- TakeFreeSlot: the lowest free slot, without walking past every assigned one.
CREATE INDEX slots_free ON slots(id) WHERE state = 'free';

-- Spendable grants only: used-up ones stay for history and would grow the scan.
CREATE INDEX traffic_grants_spendable ON traffic_grants(user_id, pool_id) WHERE remaining > 0;

-- +goose Down
DROP INDEX traffic_grants_spendable;
DROP INDEX slots_free;
DROP INDEX tariff_pools_pool;
DROP INDEX user_pools_pool;
DROP INDEX tg_notices_sent;
DROP INDEX inbound_events_created;
DROP INDEX sub_fetches_fetched;
DROP INDEX inbound_reach_inbound;
DROP INDEX inbound_reach_at;
DROP INDEX payments_tg;

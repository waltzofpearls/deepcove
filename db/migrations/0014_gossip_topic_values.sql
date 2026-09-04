-- What each node last said about each topic. One row per (topic, node); the freshest wins.
CREATE TABLE IF NOT EXISTS gossip_topic_values (
  topic          TEXT      NOT NULL,
  stable_node_id TEXT      NOT NULL,
  value          TEXT      NOT NULL,
  seen_at        TIMESTAMP NOT NULL,
  PRIMARY KEY (topic, stable_node_id)
);

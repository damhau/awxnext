-- Labels on inventories, as AWX's Inventory.labels.
CREATE TABLE inventory_labels (
    inventory_id bigint NOT NULL REFERENCES inventories(id) ON DELETE CASCADE,
    label_id bigint NOT NULL REFERENCES labels(id) ON DELETE CASCADE,
    PRIMARY KEY (inventory_id, label_id)
);

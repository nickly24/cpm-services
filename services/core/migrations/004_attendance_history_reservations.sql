CREATE TABLE IF NOT EXISTS core_attendance_history_reservations (
 organization_id BIGINT NOT NULL,
 division_id BIGINT NOT NULL,
 user_id INT NOT NULL,
 reservation_id CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 operation_key VARCHAR(100) NOT NULL,
 created_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
 PRIMARY KEY (organization_id, division_id, user_id),
 UNIQUE KEY uq_core_attendance_reservation (reservation_id),
 FOREIGN KEY (organization_id) REFERENCES organizations(id),
 FOREIGN KEY (division_id) REFERENCES divisions(id),
 FOREIGN KEY (user_id) REFERENCES auth_users(id)
);

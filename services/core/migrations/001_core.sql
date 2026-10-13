CREATE TABLE IF NOT EXISTS core_user_owners (
 user_id INT NOT NULL PRIMARY KEY, organization_id BIGINT NOT NULL,
 FOREIGN KEY(user_id) REFERENCES auth_users(id), FOREIGN KEY(organization_id) REFERENCES organizations(id)
);
CREATE TABLE IF NOT EXISTS core_group_scope (
 group_id INT NOT NULL PRIMARY KEY, organization_id BIGINT NOT NULL, division_id BIGINT NOT NULL,
 is_active BOOLEAN NOT NULL DEFAULT 1,
 UNIQUE(division_id,group_id), FOREIGN KEY(group_id) REFERENCES `groups`(id),
 FOREIGN KEY(organization_id,division_id) REFERENCES divisions(organization_id,id)
);
CREATE TABLE IF NOT EXISTS core_school_scope (
 school_id INT NOT NULL PRIMARY KEY, organization_id BIGINT NOT NULL,
 FOREIGN KEY(school_id) REFERENCES schools(id), FOREIGN KEY(organization_id) REFERENCES organizations(id)
);
CREATE TABLE IF NOT EXISTS core_direction_scope (
 direction_id INT NOT NULL PRIMARY KEY, organization_id BIGINT NOT NULL,
 is_active BOOLEAN NOT NULL DEFAULT 1,
 FOREIGN KEY(direction_id) REFERENCES directions(id), FOREIGN KEY(organization_id) REFERENCES organizations(id)
);
CREATE TABLE IF NOT EXISTS core_proctor_groups (
 user_id INT NOT NULL, division_id BIGINT NOT NULL, group_id INT NOT NULL,
 PRIMARY KEY(user_id,division_id), UNIQUE(group_id),
 FOREIGN KEY(user_id) REFERENCES auth_users(id), FOREIGN KEY(division_id,group_id) REFERENCES core_group_scope(division_id,group_id)
);
CREATE TABLE IF NOT EXISTS core_rating_locks (
 user_id INT NOT NULL, division_id BIGINT NOT NULL, created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
 PRIMARY KEY(user_id,division_id), FOREIGN KEY(user_id,division_id) REFERENCES auth_division_access(user_id,division_id)
);
CREATE TABLE IF NOT EXISTS core_lessons (
 id BIGINT AUTO_INCREMENT PRIMARY KEY, organization_id BIGINT NOT NULL, division_id BIGINT NOT NULL,
 date DATE NOT NULL, start_time CHAR(5) NOT NULL, end_time CHAR(5) NOT NULL,
 lesson_name VARCHAR(200) NOT NULL, teacher_name VARCHAR(200) NOT NULL, location VARCHAR(200) NOT NULL,
 classroom VARCHAR(50) NOT NULL, color CHAR(7) NOT NULL, is_changed BOOLEAN NOT NULL, is_in_person BOOLEAN NOT NULL,
 is_for_all BOOLEAN NOT NULL, is_public BOOLEAN NOT NULL, school_id INT NULL,
 FOREIGN KEY(organization_id,division_id) REFERENCES divisions(organization_id,id), FOREIGN KEY(school_id) REFERENCES schools(id),
 INDEX(division_id,date,start_time), CHECK(start_time<end_time)
);
CREATE TABLE IF NOT EXISTS core_class_days (
 id BIGINT AUTO_INCREMENT PRIMARY KEY, organization_id BIGINT NOT NULL, division_id BIGINT NOT NULL,
 date DATE NOT NULL, comment VARCHAR(500) NOT NULL DEFAULT '',
 UNIQUE(division_id,date), UNIQUE(division_id,id), FOREIGN KEY(organization_id,division_id) REFERENCES divisions(organization_id,id)
);
CREATE TABLE IF NOT EXISTS core_zaps (
 id BIGINT AUTO_INCREMENT PRIMARY KEY, organization_id BIGINT NOT NULL, division_id BIGINT NOT NULL, user_id INT NOT NULL,
 text TEXT NOT NULL, status ENUM('set','apr','dec') NOT NULL DEFAULT 'set', answer TEXT NOT NULL,
 UNIQUE(division_id,id), FOREIGN KEY(organization_id,division_id) REFERENCES divisions(organization_id,id), FOREIGN KEY(user_id) REFERENCES auth_users(id)
);
CREATE TABLE IF NOT EXISTS core_attendance (
 id BIGINT AUTO_INCREMENT PRIMARY KEY, division_id BIGINT NOT NULL, class_day_id BIGINT NOT NULL, user_id INT NOT NULL,
 attendance_type_id TINYINT UNSIGNED NOT NULL, zap_id BIGINT NULL,
 UNIQUE(class_day_id,user_id), FOREIGN KEY(division_id,class_day_id) REFERENCES core_class_days(division_id,id),
 FOREIGN KEY(user_id) REFERENCES auth_users(id), FOREIGN KEY(attendance_type_id) REFERENCES attendance_types(id),
 FOREIGN KEY(division_id,zap_id) REFERENCES core_zaps(division_id,id)
);
CREATE TABLE IF NOT EXISTS core_zap_dates (
 id BIGINT AUTO_INCREMENT PRIMARY KEY, zap_id BIGINT NOT NULL, date DATE NOT NULL,
 status ENUM('pending','linked','no_class_day','cancelled') NOT NULL DEFAULT 'pending',
 UNIQUE(zap_id,date), FOREIGN KEY(zap_id) REFERENCES core_zaps(id)
);
CREATE TABLE IF NOT EXISTS core_zap_images (
 id BIGINT AUTO_INCREMENT PRIMARY KEY, zap_id BIGINT NOT NULL, mime VARCHAR(20) NOT NULL, image MEDIUMBLOB NOT NULL,
 FOREIGN KEY(zap_id) REFERENCES core_zaps(id)
);
CREATE TABLE IF NOT EXISTS core_imports (
 id BIGINT AUTO_INCREMENT PRIMARY KEY, organization_id BIGINT NOT NULL, division_id BIGINT NOT NULL,
 actor_id INT NOT NULL, payload JSON NOT NULL, status ENUM('preview','completed') NOT NULL DEFAULT 'preview', report JSON NULL,
 created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
 FOREIGN KEY(organization_id,division_id) REFERENCES divisions(organization_id,id), FOREIGN KEY(actor_id) REFERENCES auth_users(id)
);
CREATE TABLE IF NOT EXISTS core_audit (
 id BIGINT AUTO_INCREMENT PRIMARY KEY, actor_id INT NULL, organization_id BIGINT NOT NULL, division_id BIGINT NULL,
 action VARCHAR(80) NOT NULL, entity_id BIGINT NOT NULL, created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
 INDEX(organization_id,id)
);
CREATE TABLE IF NOT EXISTS core_profile_extra (
 user_id INT PRIMARY KEY, tg_name VARCHAR(30) NOT NULL DEFAULT '', FOREIGN KEY(user_id) REFERENCES auth_users(id)
);
CREATE TABLE IF NOT EXISTS core_id_allocator (
 id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY
) AUTO_INCREMENT=10000;

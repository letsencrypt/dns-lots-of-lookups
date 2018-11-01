DROP TABLE IF EXISTS `results`;
DROP TABLE IF EXISTS `servers`;
DROP TABLE IF EXISTS `experiments`;

CREATE TABLE `experiments` (
	`id` INT NOT NULL AUTO_INCREMENT,
	`start` DATETIME NOT NULL,
	`end` DATETIME,
	`commandline` VARCHAR(255) NOT NULL,
	PRIMARY KEY (`id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE `servers` (
	`id` INT NOT NULL AUTO_INCREMENT,
	`address` VARCHAR(255) NOT NULL,
	`experimentID` INT NOT NULL,
	PRIMARY KEY (`id`),
	CONSTRAINT `servers_experimentID_experiments` FOREIGN KEY (`experimentID`) REFERENCES `experiments` (`id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE `results` (
	`id` INT NOT NULL AUTO_INCREMENT,
	`name` VARCHAR(255) NOT NULL,
	`type` INT NOT NULL,
	`error` MEDIUMBLOB DEFAULT NULL,
	`serverID` INT NOT NULL,
	`experimentID` INT NOT NULL,
	PRIMARY KEY (`id`),
	CONSTRAINT `results_serverID_servers` FOREIGN KEY (`serverID`) REFERENCES servers (`id`),
	CONSTRAINT `results_experimentID_experiments` FOREIGN KEY (`experimentID`) REFERENCES experiments (`id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

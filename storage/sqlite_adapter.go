package storage

import (
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"time"

	"gorm.io/driver/sqlite" // Sqlite driver based on GGO

	// "github.com/glebarez/sqlite" // Pure go SQLite driver, checkout https://github.com/glebarez/sqlite for details

	"gorm.io/gorm"
)

type ChangeCount map[string]int32

// Scan implements the Scanner interface.
func (n *ChangeCount) Scan(value any) error {
	b, ok := value.([]byte)
	if !ok {
		return fmt.Errorf("convert value to []byte failed")
	}
	return json.Unmarshal(b, n)
}

// Value implements the driver Valuer interface.
func (n ChangeCount) Value() (driver.Value, error) {
	return json.Marshal(n)
}

// TODO able to open sqlite for readonly?

// is_deleted || is_discarded can be removed from storage any time
type DBRecord struct {
	Key                string      `gorm:"index;column:key"`
	Value              string      `gorm:"column:value"`
	MachineID          string      `gorm:"column:machine_id"`
	Offset             int64       `gorm:"column:offset"`
	PrevMachineID      string      `gorm:"column:prev_machine_id"`
	Seq                uint64      `gorm:"column:seq"`
	CurrentLogGid      string      `gorm:"uniqueIndex;column:gid"`
	PrevLogGid         string      `gorm:"column:prev_log_gid"`
	IsDiscarded        bool        `gorm:"column:is_discarded"`
	IsDeleted          bool        `gorm:"column:is_deleted"`
	MachineChangeCount ChangeCount `gorm:"column:change_count"`
	Num                int64       `gorm:"num"`
	PrevNum            int64       `gorm:"prev_num"`
	CreatedAt          time.Time
	UpdatedAt          time.Time
	DeletedAt          sql.NullTime `gorm:"index"`
}

// call newLogProgress to make instance
type LogProgress struct {
	Offset    int64  `gorm:"column:offset"` // HeaderSize should be used as initial value
	Num       int64  `gorm:"column:num"`
	Gid       string `gorm:"column:gid"`
	MachineID string `gorm:"uniqueIndex;column:machine_id"`
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt sql.NullTime `gorm:"index"`
}

func newLogProgress(machineID string) *LogProgress {
	return &LogProgress{MachineID: machineID, Offset: HeaderSize}
}

func (r *DBRecord) Visible() bool {
	return !r.IsDeleted && !r.IsDiscarded
}

func (r *DBRecord) Changes(machineID string) int32 {
	if r.MachineChangeCount == nil {
		return 0
	}
	if changes, ok := r.MachineChangeCount[machineID]; ok {
		return changes
	}
	return 0
}

func (r *DBRecord) AddChange(machineID string, changes int32) map[string]int32 {
	m := make(map[string]int32)
	if r.MachineChangeCount != nil {
		for k, v := range r.MachineChangeCount {
			m[k] = v
		}
	}
	m[machineID] += changes
	return m
}

// SqliteAdapter ...
type SqliteAdapter struct {
	db        *gorm.DB
	workingDB *gorm.DB
}

func (s *SqliteAdapter) Transaction(f func(s *SqliteAdapter) error) error {
	return s.workingDB.Transaction(func(tx *gorm.DB) error {
		return f(&SqliteAdapter{db: s.db, workingDB: tx})
	})
}

func (s *SqliteAdapter) Init(dbFile string) error {
	l := gormLoggerImpl{}
	l.Init(logger)
	db, err := gorm.Open(sqlite.Open(dbFile), &gorm.Config{
		SkipDefaultTransaction: true,
		Logger:                 &l,
	})
	if err != nil {
		return err
	}

	err = db.AutoMigrate(&DBRecord{})
	if err != nil {
		return err
	}
	err = db.AutoMigrate(&LogProgress{})
	if err != nil {
		return err
	}

	s.db = db
	s.workingDB = db
	return nil
}

func (s *SqliteAdapter) Close() error {
	if s.db != nil {
		db, err := s.db.DB()
		if err != nil {
			return err
		}
		err = db.Close()
		if err != nil {
			return err
		}
		s.db = nil
	}
	return nil
}

func (s *SqliteAdapter) updateLogProgress(progress *LogProgress) error {
	records := []*LogProgress{}
	result := s.workingDB.Model(&LogProgress{}).Find(&records, "machine_id = ?", progress.MachineID)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected > 0 {
		return s.workingDB.Model(&LogProgress{}).Where("machine_id = ?", progress.MachineID).Updates(progress).Error
	} else {
		return s.workingDB.Model(&LogProgress{}).Create(progress).Error
	}
}

//TODO: whether to have soft deletion enabled

func (s *SqliteAdapter) Has(gid string) (bool, error) {
	records := []DBRecord{}
	result := s.workingDB.Model(&DBRecord{}).Find(&records, "gid = ?", gid)
	if result.Error != nil {
		return false, result.Error
	}

	return result.RowsAffected > 0, nil
}

func (s *SqliteAdapter) Add(record *DBRecord) error {
	has, err := s.Has(record.CurrentLogGid)
	if err != nil {
		return err
	}
	if has {
		return fmt.Errorf("node exists")
	}

	return s.Transaction(func(s2 *SqliteAdapter) error {
		if err := s2.workingDB.Model(&DBRecord{}).Create(record).Error; err != nil {
			return err
		}
		return s2.updateLogProgress(&LogProgress{MachineID: record.MachineID, Offset: record.Offset,
			Gid: record.CurrentLogGid, Num: record.Num})
	})
}

func (s *SqliteAdapter) Replace(old string, new *DBRecord) error {
	has, err := s.Has(old)
	if err != nil {
		return err
	}
	if !has {
		return fmt.Errorf("node not exist")
	}

	has, err = s.Has(new.CurrentLogGid)
	if err != nil {
		return err
	}
	if has {
		return fmt.Errorf("new node already exist")
	}

	return s.Transaction(func(s2 *SqliteAdapter) error {
		if err := s2.delNode(old); err != nil {
			return err
		}
		if err := s2.workingDB.Model(&DBRecord{}).Create(new).Error; err != nil {
			return err
		}
		return s2.updateLogProgress(&LogProgress{MachineID: new.MachineID, Offset: new.Offset,
			Gid: new.CurrentLogGid, Num: new.Num})
	})
}

func (s *SqliteAdapter) delNode(gid string) error {
	return s.workingDB.Model(&DBRecord{}).Where("gid = ?", gid).Delete(&DBRecord{CurrentLogGid: gid}).Error
}

func (s *SqliteAdapter) GetByKey(key string) ([]*DBRecord, error) {
	records := []*DBRecord{}
	result := s.workingDB.Model(&DBRecord{}).Where("key = ?", key).Find(&records)
	if result.Error != nil {
		return nil, result.Error
	}
	return records, nil
}

func (s *SqliteAdapter) GetByGid(gid string) (*DBRecord, error) {
	rec := DBRecord{}
	result := s.workingDB.Model(&DBRecord{}).Where("gid = ?", gid).First(&rec)
	if result.Error != nil {
		return nil, result.Error
	}
	return &rec, nil
}

func (s *SqliteAdapter) AllNodes() ([]*DBRecord, error) {
	records := []*DBRecord{}
	result := s.workingDB.Model(&DBRecord{}).Find(&records)
	if result.Error != nil {
		return nil, result.Error
	}
	return records, nil
}

func (s *SqliteAdapter) Processes() ([]*LogProgress, error) {
	records := []*LogProgress{}
	result := s.workingDB.Model(&LogProgress{}).Find(&records)
	if result.Error != nil {
		return nil, result.Error
	}
	return records, nil
}

func (s *SqliteAdapter) Merge(other ReadOnlyNodeStorage) error {
	return fmt.Errorf("unsupported")
}

func _() {
	var _ NodeStorage = &SqliteAdapter{}
}

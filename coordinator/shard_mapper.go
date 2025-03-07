/*
Copyright 2022 Huawei Cloud Computing Technologies Co., Ltd.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

 http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package coordinator

import (
	"context"
	"fmt"
	"math"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/openGemini/openGemini/engine/executor"
	"github.com/openGemini/openGemini/engine/executor/spdy/transport"
	"github.com/openGemini/openGemini/engine/hybridqp"
	"github.com/openGemini/openGemini/lib/logger"
	meta "github.com/openGemini/openGemini/lib/metaclient"
	"github.com/openGemini/openGemini/lib/netstorage"
	"github.com/openGemini/openGemini/lib/rand"
	"github.com/openGemini/openGemini/lib/record"
	"github.com/openGemini/openGemini/lib/statisticsPusher/statistics"
	"github.com/openGemini/openGemini/lib/tracing"
	"github.com/openGemini/openGemini/open_src/influx/influxql"
	meta2 "github.com/openGemini/openGemini/open_src/influx/meta"
	"github.com/openGemini/openGemini/open_src/influx/query"
	"go.uber.org/zap"
)

// ClusterShardMapper implements a ShardMapper for Remote shards.
type ClusterShardMapper struct {
	//Node   *meta.Node
	Logger *logger.Logger
	// Remote execution timeout
	Timeout time.Duration
	meta.MetaClient
	NetStore  netstorage.Storage
	SeriesKey []byte
}

func (csm *ClusterShardMapper) MapShards(sources influxql.Sources, t influxql.TimeRange, opt query.SelectOptions, condition influxql.Expr) (query.ShardGroup, error) {
	a := &ClusterShardMapping{
		ShardMapper: csm,
		ShardMap:    make(map[Source]map[uint32][]uint64),
		MetaClient:  csm.MetaClient,
		Timeout:     csm.Timeout,
		//Node:        csm.Node,
		NetStore: csm.NetStore,
		Logger:   csm.Logger.With(zap.String("shardMapping", "cluster")),
	}

	tmin := time.Unix(0, t.MinTimeNano())
	tmax := time.Unix(0, t.MaxTimeNano())
	if err := csm.mapShards(a, sources, tmin, tmax, condition, &opt); err != nil {
		return nil, err
	}
	a.MinTime, a.MaxTime = tmin, tmax

	return a, nil
}

func (csm *ClusterShardMapper) Close() error {
	return nil
}

func (csm *ClusterShardMapper) GetSeriesKey() []byte {
	return csm.SeriesKey
}

func (csm *ClusterShardMapper) mapShards(a *ClusterShardMapping, sources influxql.Sources, tmin, tmax time.Time, condition influxql.Expr, opt *query.SelectOptions) error {
	for _, s := range sources {
		switch s := s.(type) {
		case *influxql.Measurement:
			source := Source{
				Database:        s.Database,
				RetentionPolicy: s.RetentionPolicy,
			}
			var shardKeyInfo *meta2.ShardKeyInfo
			dbi, err := csm.MetaClient.Database(s.Database)
			if err != nil {
				return err
			}
			if len(dbi.ShardKey.ShardKey) > 0 {
				shardKeyInfo = &dbi.ShardKey
			}
			measurements, err := csm.MetaClient.GetMeasurements(s)
			if err != nil {
				return err
			}
			if len(measurements) == 0 {
				continue // meta.ErrMeasurementNotFound(s.Name)
			}

			// Retrieve the list of shards for this database. This list of
			// shards is always the same regardless of which measurement we are
			// using.
			if _, ok := a.ShardMap[source]; !ok {
				groups, err := csm.MetaClient.ShardGroupsByTimeRange(s.Database, s.RetentionPolicy, tmin, tmax)
				if err != nil {
					return err
				}

				if len(groups) == 0 {
					a.ShardMap[source] = nil
					continue
				}

				shardIDsByPtID := make(map[uint32][]uint64)
				for i, g := range groups {
					gTimeRange := influxql.TimeRange{Min: g.StartTime, Max: g.EndTime}
					if i == 0 {
						a.ShardsTimeRage = gTimeRange
					} else {
						if a.ShardsTimeRage.Min.After(gTimeRange.Min) {
							a.ShardsTimeRage.Min = gTimeRange.Min
						}
						if gTimeRange.Max.After(a.ShardsTimeRage.Max) {
							a.ShardsTimeRage.Max = gTimeRange.Max
						}
					}

					if shardKeyInfo == nil {
						shardKeyInfo = measurements[0].GetShardKey(groups[i].ID)
					}

					aliveShardIdxes := csm.MetaClient.GetAliveShards(s.Database, &groups[i])
					var shs []meta2.ShardInfo
					if opt.HintType == hybridqp.FullSeriesQuery || opt.HintType == hybridqp.SpecificSeriesQuery {
						shs, csm.SeriesKey = groups[i].TargetShardsHintQuery(s.Name, measurements[0], condition, opt, aliveShardIdxes)
					} else {
						shs = groups[i].TargetShards(s.Name, measurements[0], shardKeyInfo, condition, aliveShardIdxes)
					}

					for shIdx := range shs {
						var ptID uint32
						if len(shs[shIdx].Owners) > 0 {
							ptID = shs[shIdx].Owners[rand.Intn(len(shs[shIdx].Owners))]
						} else {
							csm.Logger.Warn("shard has no owners", zap.Uint64("shardID", shs[shIdx].ID))
							continue
						}
						shardIDsByPtID[ptID] = append(shardIDsByPtID[ptID], shs[shIdx].ID)
					}
				}
				a.ShardMap[source] = shardIDsByPtID
			}
		case *influxql.SubQuery:
			if err := csm.mapShards(a, s.Statement.Sources, tmin, tmax, condition, opt); err != nil {
				return err
			}
		}
	}
	return nil
}

// ClusterShardMapping maps data sources to a list of shard information.
type ClusterShardMapping struct {
	//Node        *meta.Node
	ShardMapper *ClusterShardMapper
	NetStore    netstorage.Storage

	MetaClient meta.MetaClient

	// Remote execution timeout
	Timeout time.Duration

	ShardMap map[Source]map[uint32][]uint64

	// MinTime is the minimum time that this shard mapper will allow.
	// Any attempt to use a time before this one will automatically result in using
	// this time instead.
	MinTime time.Time

	// MaxTime is the maximum time that this shard mapper will allow.
	// Any attempt to use a time after this one will automatically result in using
	// this time instead.
	MaxTime time.Time

	ShardsTimeRage influxql.TimeRange
	Logger         *logger.Logger
}

func (csm *ClusterShardMapping) ShardsTimeRange() influxql.TimeRange {
	return csm.ShardsTimeRage
}

func (csm *ClusterShardMapping) NodeNumbers() int {
	nods, _ := csm.MetaClient.DataNodes()
	if len(nods) == 0 {
		return 1
	}
	return len(nods)
}

func (csm *ClusterShardMapping) getSchema(database string, retentionPolicy string, mst string) (map[string]int32, map[string]struct{}, error) {
	startTime := time.Now()

	var metaFields map[string]int32
	var metaDimensions map[string]struct{}
	var err error

	for {
		metaFields, metaDimensions, err = csm.MetaClient.Schema(database, retentionPolicy, mst)
		if err != nil {
			if strings.Contains(err.Error(), netstorage.ErrPartitionNotFound.Error()) || strings.Contains(err.Error(), meta2.ErrDBPTClose.Error()) || strings.Contains(err.Error(), "connection reset by peer") || strings.Contains(err.Error(), "connection refused") || strings.Contains(err.Error(), "broken pipe") || strings.Contains(err.Error(), "read message type: EOF") ||
				strings.Contains(err.Error(), "write: connection timed out") {
				if time.Since(startTime).Seconds() < DMLTimeOutSecond {
					csm.Logger.Warn("retry get schema", zap.String("database", database), zap.String("measurement", mst))
					time.Sleep(DMLRetryInternalMillisecond * time.Millisecond)
					continue
				} else {
					panic(err)
				}
			} else {
				csm.Logger.Warn("get field schema failed from metaClient", zap.String("database", database),
					zap.String("measurement", mst), zap.Any("err", err))
				return nil, nil, fmt.Errorf("get schema failed")
			}
		}
		break
	}
	return metaFields, metaDimensions, err
}

func (csm *ClusterShardMapping) FieldDimensions(m *influxql.Measurement) (fields map[string]influxql.DataType, dimensions map[string]struct{}, schema *influxql.Schema, err error) {
	source := Source{
		Database:        m.Database,
		RetentionPolicy: m.RetentionPolicy,
	}

	shardIDsByNodeID := csm.ShardMap[source]
	if shardIDsByNodeID == nil {
		return nil, nil, nil, nil
	}
	fields = make(map[string]influxql.DataType)
	dimensions = make(map[string]struct{})
	schema = &influxql.Schema{MinTime: math.MaxInt64, MaxTime: math.MinInt64}
	measurements, err := csm.MetaClient.GetMeasurements(m)
	if err != nil {
		return nil, nil, nil, err
	}
	for i := range measurements {
		var metaFields map[string]int32
		var metaDimensions map[string]struct{}
		metaFields, metaDimensions, err = csm.getSchema(m.Database, m.RetentionPolicy, measurements[i].Name)
		if err != nil {
			return nil, nil, nil, err
		}
		if metaFields == nil && metaDimensions == nil {
			continue
		}
		for k, ty := range metaFields {
			fields[k] = record.ToInfluxqlTypes(int(ty))
		}
		for k := range metaDimensions {
			dimensions[k] = struct{}{}
		}
	}

	return
}

func (csm *ClusterShardMapping) MapType(m *influxql.Measurement, field string) influxql.DataType {
	measurements, err := csm.MetaClient.GetMeasurements(m)
	if err != nil {
		return influxql.Unknown
	}

	for i := range measurements {
		metaFields, metaDimensions, err := csm.getSchema(m.Database, m.RetentionPolicy, measurements[i].Name)
		if err != nil {
			return influxql.Unknown
		}
		for k, ty := range metaFields {
			if k == field {
				return record.ToInfluxqlTypes(int(ty))
			}
		}
		for k := range metaDimensions {
			if k == field {
				return influxql.Tag
			}
		}
	}
	return influxql.Unknown
}

func (csm *ClusterShardMapping) MapTypeBatch(m *influxql.Measurement, fields map[string]influxql.DataType, schema *influxql.Schema) error {
	measurements, err := csm.MetaClient.GetMeasurements(m)
	if err != nil {
		return err
	}
	for i := range measurements {
		metaFields, metaDimensions, err := csm.getSchema(m.Database, m.RetentionPolicy, measurements[i].Name)
		if err != nil {
			return err
		}

		for k := range fields {
			ft, ftOk := metaFields[k]
			_, dtOK := metaDimensions[k]

			if !(ftOk || dtOK) {
				fields[k] = influxql.Unknown
				continue
			}

			if ftOk && dtOK {
				return fmt.Errorf("column (%s) in measurement (%s) in both fields and tags", k, measurements[i].Name)
			}

			if ftOk {
				fields[k] = record.ToInfluxqlTypes(int(ft))
			} else {
				fields[k] = influxql.Tag
			}
		}
	}
	return nil
}

func (csm *ClusterShardMapping) CreateLogicalPlan(ctx context.Context, sources influxql.Sources, schema hybridqp.Catalog) (hybridqp.QueryNode, error) {
	ctxValue := ctx.Value(query.QueryDurationKey)
	if ctxValue != nil {
		qDuration := ctxValue.(*statistics.SQLSlowQueryStatistics)
		if qDuration != nil {
			schema.Options().(*query.ProcessorOptions).Query = qDuration.Query
			start := time.Now()
			defer func() {
				qDuration.AddDuration("LocalIteratorDuration", time.Since(start).Nanoseconds())
			}()
		}
	}
	shardsMapByNode := make(map[uint64]map[uint32][]uint64) // {"nodeId": {"ptId": []shardId } }
	sourcesMapByPtId := make(map[uint32]influxql.Sources)   // {"ptId": influxql.Sources }

	opts := schema.Options().(*query.ProcessorOptions)

	for _, src := range sources {
		switch src := src.(type) {
		case *influxql.Measurement:
			source := Source{
				Database:        src.Database,
				RetentionPolicy: src.RetentionPolicy,
			}
			shardIDsByDBPT := csm.ShardMap[source]
			if shardIDsByDBPT == nil {
				continue
			}

			ptView, err := csm.MetaClient.DBPtView(source.Database)
			if err != nil {
				return nil, err
			}
			measurements, err := csm.MetaClient.GetMeasurements(src)
			if err != nil {
				return nil, err
			}
			var srcs influxql.Sources
			for i := range measurements {
				clone := src.Clone()
				clone.Regex = nil
				clone.Name = measurements[i].Name
				srcs = append(srcs, clone)
			}
			for pId, sIds := range shardIDsByDBPT {
				nodeID := ptView[pId].Owner.NodeID
				if _, ok := shardsMapByNode[nodeID]; !ok {
					shardIDsByPtID := make(map[uint32][]uint64)
					shardIDsByPtID[pId] = sIds
					shardsMapByNode[nodeID] = shardIDsByPtID
					sourcesMapByPtId[pId] = srcs
				} else {
					sourcesMapByPtId[pId] = append(sourcesMapByPtId[pId], srcs...)
				}
			}
		case *influxql.SubQuery:
			panic("subquery is not supported.")
		default:
			panic("unknown measurement.")
		}
	}

	// Override the time constraints if they don't match each other.
	if !csm.MinTime.IsZero() && opts.StartTime < csm.MinTime.UnixNano() {
		opts.StartTime = csm.MinTime.UnixNano()
	}
	if !csm.MaxTime.IsZero() && opts.EndTime > csm.MaxTime.UnixNano() {
		opts.EndTime = csm.MaxTime.UnixNano()
	}

	wg := sync.WaitGroup{}
	eTraits := make([]hybridqp.Trait, 0, len(shardsMapByNode))
	var muList = sync.Mutex{}
	errs := make([]error, 0, len(shardsMapByNode))

	for nodeID, shardsByPtId := range shardsMapByNode {
		for pId, sIds := range shardsByPtId {
			wg.Add(1)
			go func(nodeID uint64, ptID uint32, shardIDs []uint64) {
				defer wg.Done()
				src := sourcesMapByPtId[ptID]
				rq, err := csm.makeRemoteQuery(ctx, src, *opts, nodeID, ptID, shardIDs)
				if err != nil {
					muList.Lock()
					errs = append(errs, err)
					muList.Unlock()
					return
				}

				muList.Lock()
				opts.Sources = src
				eTraits = append(eTraits, rq)
				muList.Unlock()
			}(nodeID, pId, sIds)
		}
	}

	wg.Wait()
	for _, err := range errs {
		if err == nil {
			continue
		}

		csm.Logger.Error("failed to createLogicalPlan", zap.Error(err))
		if !strings.Contains(err.Error(), netstorage.ErrPartitionNotFound.Error()) {
			err = nil
			continue
		}
		return nil, err
	}
	if schema.Options().(*query.ProcessorOptions).Sources == nil {
		return nil, nil
	}

	var plan hybridqp.QueryNode
	var pErr error

	builder := executor.NewLogicalPlanBuilderImpl(schema)

	// push down to chunk reader.
	plan, pErr = builder.CreateSeriesPlan()
	if pErr != nil {
		return nil, pErr
	}

	plan, pErr = builder.CreateMeasurementPlan(plan)
	if pErr != nil {
		return nil, pErr
	}

	//todo:create scanner plan
	plan, pErr = builder.CreateScanPlan(plan)
	if pErr != nil {
		return nil, pErr
	}

	plan, pErr = builder.CreateShardPlan(plan)
	if pErr != nil {
		return nil, pErr
	}

	plan, pErr = builder.CreateNodePlan(plan, eTraits)
	if pErr != nil {
		return nil, pErr
	}

	return plan.(executor.LogicalPlan), pErr
}

func (csm *ClusterShardMapping) makeRemoteQuery(ctx context.Context, src influxql.Sources, opt query.ProcessorOptions,
	nodeID uint64, ptID uint32, shardIDs []uint64) (*executor.RemoteQuery, error) {
	m, ok := src[0].(*influxql.Measurement)
	if !ok {
		return nil, fmt.Errorf("invalid sources, exp: *influxql.Measurement, got: %s", reflect.TypeOf(src[0]))
	}

	opt.Sources = src

	analyze := false
	if span := tracing.SpanFromContext(ctx); span != nil {
		analyze = true
	}

	node, err := csm.MetaClient.DataNode(nodeID)
	if err != nil {
		return nil, err
	}

	transport.NewNodeManager().Add(nodeID, node.TCPHost)
	rq := &executor.RemoteQuery{
		Database: m.Database,
		PtID:     ptID,
		NodeID:   nodeID,
		ShardIDs: shardIDs,
		Opt:      opt,
		Analyze:  analyze,
		Node:     nil,
	}
	return rq, nil
}

func (csm *ClusterShardMapping) LogicalPlanCost(m *influxql.Measurement, opt query.ProcessorOptions) (hybridqp.LogicalPlanCost, error) {
	return hybridqp.LogicalPlanCost{}, nil
}

// Close clears out the list of mapped shards.
func (csm *ClusterShardMapping) Close() error {
	csm.ShardMap = nil
	return nil
}

func (csm *ClusterShardMapping) GetSources(sources influxql.Sources) influxql.Sources {
	var srcs influxql.Sources
	for _, src := range sources {
		switch src := src.(type) {
		case *influxql.Measurement:
			measurements, err := csm.MetaClient.GetMeasurements(src)
			if err != nil {
				return nil
			}
			for i := range measurements {
				clone := src.Clone()
				clone.Regex = nil
				clone.Name = measurements[i].Name
				srcs = append(srcs, clone)
			}
		case *influxql.SubQuery:
			srcs = append(srcs, src)
		default:
			panic("unknown measurement.")
		}
	}
	return srcs
}

// Source contains the database and retention policy source for data.
type Source struct {
	Database        string
	RetentionPolicy string
}

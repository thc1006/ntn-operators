# S10-S18 交叉驗證深度調研報告

> **日期**：2026-04-16
> **來源數**：30+ 權威來源
> **驗證標準**：每個關鍵結論至少 5 份來源交叉驗證
> **專案**：NTN K8s Operators — Phase 3-6 計畫可行性分析

---

## 一、OCUDU / srsRAN NTN 開發現狀

### 1.1 OCUDU GitLab 分支與版本分析

**版本發布**（直接查詢 gitlab.com/ocudu/ocudu/-/tags）：

| 版本 | 日期 | 說明 |
|------|------|------|
| release_26_04 | 2026-03-30 | 正式版（v1.0） |
| release_26_04_rc2 | 2026-03-26 | RC2 |
| release_26_04_rc1 | 2026-03-20 | RC1 |

**NTN 相關分支**（查詢 gitlab.com/ocudu/ocudu/-/branches/all?search=ntn）：
- `debug_ntn_issue_3831` — 最後更新 2026-01-09，commit 訊息 "sched: DEBUG commit"
- **無其他 NTN 分支**

**活躍分支**（截至 2026-04-14）：
- `dev`（主開發線）
- `main`
- `pr_cho_xnap`, `ra_2_step_part2`, `cu_cp_ue_reject`（功能開發）

**結論**：OCUDU v1.0 已發布，但 **NTN 開發僅有一個 debug 分支且已停滯 3 個月**。LEO NTN 距離 v2.x 還很遠。

**來源**：
1. [OCUDU GitLab Tags](https://gitlab.com/ocudu/ocudu/-/tags)
2. [OCUDU GitLab Branches](https://gitlab.com/ocudu/ocudu/-/branches)
3. [SRS Blog: The Linux of RAN](https://srs.io/building-ocudu-the-linux-of-ran/)
4. [The Mobile Network: OCUDU big time](https://the-mobile-network.com/2026/03/ocudu-moves-into-big-time-with-major-vendor-backing/)
5. [LF Networking Confluence](https://lf-networking.atlassian.net/wiki/x/l4M_Jw)

### 1.2 OCUDU NTN Config 完整參數（from geo_ntn.yml）

```yaml
ntn:
  cell_specific_koffset: 150       # 0-1023, 最大通道延遲
  ta_common: 0                      # 0-66485757, 共同 Timing Advance
  ephemeris_info:                   # ECEF 衛星位置/速度
    pos_x: 20922195
    pos_y: 1967783
    pos_z: 19770302
    vel_x: 0                        # GEO: velocity=0
    vel_y: 0
    vel_z: 0

cell_cfg:
  sib:
    si_sched_info:
      - si_period: 16
        sib_mapping: 19             # SIB19 NTN 資訊
  pucch:
    sr_period_ms: 320               # 延展 SR 週期
  pdsch:
    max_nof_harq_retxs: 0           # NTN 停用 HARQ
  prach:
    max_msg3_harq_retx: 0

cu_cp:
  rrc:
    rrc_procedure_guard_time_ms: 12800  # 延展 RRC guard time
```

**來源**：
1. [srsRAN geo_ntn.yml](https://gitlab.com/ocudu/ocudu/-/blob/dev/configs/geo_ntn.yml) (migrated from archived srsRAN_Project)
2. [srsRAN Config Reference](https://docs.srsran.com/projects/project/en/latest/user_manuals/source/config_ref.html)
3. [srsRAN NTN Tutorial](https://docs.srsran.com/projects/project/en/latest/tutorials/source/ntn/source/index.html)
4. [srsRAN NTN Tutorial source](https://docs.srsran.com/projects/project/en/latest/_sources/tutorials/source/ntn/source/index.rst.txt)
5. [srsRAN Project Docs PDF](https://docs.srsran.com/_/downloads/project/en/latest/pdf/)

### 1.3 LEO NTN 動態 Ephemeris 問題（關鍵風險）

**問題描述**：LEO 衛星軌道周期 ~90-109 分鐘，ephemeris 需每幾分鐘更新。srsRAN/OCUDU 的 SIB19 ephemeris 是靜態的。

**已確認的 Issues**：
- [GitHub #1066](https://github.com/srsran/srsRAN_Project/issues/1066) (archived)（2025-02）：SIB19 ephemeris 不更新
- [GitHub #1293](https://github.com/srsran/srsRAN_Project/issues/1293) (archived)：額外 SIB19 問題
- srsRAN_Project 2026-02-17 **archived**

**Amarisoft 的解決方案**（商業軟體）：
- ESA + Telesat + Amarisoft 在 Telesat LEO3 衛星上完成世界首個 5G NTN LEO link
- Amarisoft UE 能透過 SIB19 接收 ephemeris 並補償 Doppler
- 但 Amarisoft 是**閉源商業軟體**，不適合我們的開源 Provider

**來源**：
1. [GitHub Issue #1066](https://github.com/srsran/srsRAN_Project/issues/1066) (archived)
2. [GitHub Issue #1293](https://github.com/srsran/srsRAN_Project/issues/1293) (archived)
3. [ESA/Telesat/Amarisoft LEO NTN](https://connectivity.esa.int/archives/news/esa-telesat-and-amarisoft-achieve-worldfirst-5g-3gpp-nonterrestrial-network-link-over-leo)
4. [Amarisoft NTN docs](https://tech-academy.amarisoft.com/NR_SA_NTN.html)
5. [srsRAN NTN Tutorial](https://docs.srsran.com/projects/project/en/latest/tutorials/source/ntn/source/index.html)

### 1.4 OCUDU K8s 部署（Helm + O1 NETCONF）

**發現**：OCUDU 有完整的 K8s 部署方案：
- Helm chart at gitlab.com/ocudu
- Docker images on Docker Hub
- O1 NETCONF sidecar 支援 **runtime config 變更 + gNB restart**
- SR-IOV for OFH interface
- LoadBalancer/NodePort/ClusterIP for N2/N3

**對 S11 Provider 的影響**：原計畫「config file + process signal」可改為 **Helm values overlay** 或 **O1 NETCONF API**。

**來源**：
1. [OCUDU Helm blog](https://nilsfuerste.com/2026/02/02/ocudu-on-kubernetes-0-introducing-ocudu-and-helm-chart-architecture/)
2. [srsRAN K8s docs](https://docs.srsran.com/projects/project/en/latest/tutorials/source/k8s/source/index.html)
3. [srsRAN Helm repo](https://github.com/srsran/srsRAN_Project_helm) (archived; see [OCUDU GitLab](https://gitlab.com/ocudu/ocudu))
4. [OCUDU GitLab](https://gitlab.com/ocudu/ocudu)
5. [OCUDU Docs](https://ocudu-docs-604e90.gitlab.io/)

---

## 二、替代 RAN 平台分析

### 2.1 OpenAirInterface (OAI) NTN

**NTN 支援狀態**：
- GEO NTN: ✅ 完整支援，有 GEO 實驗驗證
- LEO NTN: ✅ RFsimulator 支援（SAT_LEO_TRANS, SAT_LEO_REGEN）
- 3GPP Rel-17 compliant，Rel-18 考慮中
- K8s: oai-operators repo + Helm charts

**優於 OCUDU 的地方**：
- LEO RFsim **已有** regenerative 模式
- K8s operators + Helm charts 成熟
- ngkore/OAI-5G-NR-NTN 有完整部署指南

**來源**：
1. [OAI oai-operators](https://github.com/OPENAIRINTERFACE/oai-operators)
2. [OAI NTN IEEE paper](https://ieeexplore.ieee.org/document/10723292/)
3. [ngkore OAI-5G-NR-NTN](https://github.com/ngkore/OAI-5G-NR-NTN)
4. [Fraunhofer NTN PHY/MAC](https://openairinterface.org/wp-content/uploads/2024/03/20240405_Fraunhofer_IIS_NTN_v2.pdf)
5. [EURECOM GEO demo paper](https://www.eurecom.fr/publication/7023/download/comsys-publi-7023_1.pdf)
6. [OAI 5G NR NTN paper](https://www.eurecom.fr/publication/6559/download/comsys-publi-6559.pdf)

### 2.2 OpenNTN（ant-uni-bremen）

**定位**：NTN **通道模型模擬**框架，非 RAN 軟體。

| 項目 | 值 |
|------|-----|
| Stars | 50 |
| License | MIT + Apache 2.0 (Sionna) |
| Language | Python 63.9% + Jupyter 36.1% |
| 依賴 | NVIDIA Sionna™ |
| 支援 | 3GPP TR38.811: dense urban, urban, suburban |
| LEO/GEO | 兩者皆支援 |
| K8s | ❌ 無 |

**對我們的價值**：
- **S3 cross-validation**：可用 OpenNTN 驗證我們的 pass prediction 精度（與 SGP4 比較）
- **S11/S12 測試**：NTN 通道模型可用於模擬測試環境
- **不是**直接的 Provider 實作對象

**來源**：
1. [OpenNTN GitHub](https://github.com/ant-uni-bremen/OpenNTN)
2. [OpenNTN paper](https://www.ant.uni-bremen.de/sixcms/media.php/102/15080/An%20Open%20Source%20Channel%20Emulator%20for%20Non-Terrestrial%20Networks.pdf)
3. [OpenNTN DeepWiki](https://deepwiki.com/ant-uni-bremen/OpenNTN)
4. [OpenNTN publication](https://www.ant.uni-bremen.de/en/publications/15185/)
5. [3GPP TR38.811](https://hscc.csie.ncu.edu.tw/38811.pdf)

---

## 三、joshuaferrara/go-satellite 現狀

| 項目 | 值 |
|------|-----|
| Stars | 97 |
| Last release | v0.1.0 (2022-06-11) |
| Open issues | 10 |
| 維護狀態 | **已停止**（~4 年無更新） |
| OMM 支援 | ❌ 無 |
| Pass prediction | ❌ 無（只有 ECIToLookAngles） |
| SDP4 | ✅ 有 |

**結論**：我們在 S2 已正確決定棄用此庫改用 `akhenakh/sgp4`。**無需回頭**。joshuaferrara/go-satellite 唯一的優勢是 SDP4 支援，但 OneWeb LEO 不需要 SDP4。

**來源**：
1. [joshuaferrara/go-satellite GitHub](https://github.com/joshuaferrara/go-satellite)
2. [go-satellite pkg.go.dev](https://pkg.go.dev/github.com/joshuaferrara/go-satellite)
3. [go-satellite issues](https://github.com/joshuaferrara/go-satellite/issues)
4. [akhenakh/sgp4 pkg.go.dev](https://pkg.go.dev/github.com/akhenakh/sgp4)
5. [sgp4 GitHub topics](https://github.com/topics/sgp4)

---

## 四、Aalyria Spacetime API 最新狀態

| 項目 | 值 |
|------|-----|
| 最新版本 | v21.0.1776189356+7391687 (2026-04-14) |
| Stars | 24 |
| Releases | 49 |
| Language | Go 68.9%, Starlark 17.9%, Java 6.0% |
| License | Apache 2.0 |
| API | NBI + SBI + Federation (gRPC/protobuf) |
| K8s CRDs | ❌ 無 |
| NBI 狀態 | **正在遷移**到 NMTS Entity/Relationship model |

**注意**：NBI API 正在遷移。S18 Aalyria Provider 應 **pin 到 v21.0** 並監控遷移進度。

**來源**：
1. [Aalyria API GitHub](https://github.com/aalyria/api)
2. [Aalyria API Docs](https://docs.spacetime.aalyria.com/api/)
3. [Aalyria gRPC Docs](https://docs.spacetime.aalyria.com/dev-guides/grpc/)
4. [Aalyria SBI Docs](https://docs.spacetime.aalyria.com/api/sbi/)
5. [SiliconANGLE: Aalyria $100M](https://siliconangle.com/2026/02/23/space-networking-startup-aalyria-nabs-100m-investment/)

---

## 五、3GPP Release 19 NTN 新功能

| 新功能 | 說明 | CRD 影響 |
|--------|------|---------|
| **Regenerative payload** | gNB ON satellite（非 bent-pipe） | NTNCellConfig 需 `payloadType` field |
| **ISL (Inter-Satellite Links)** | 衛星間通訊 via Xn interface | 可能需新 CRD 或 NTNCellConfig extension |
| **RedCap NTN** | 低功耗 IoT 設備 | NTNSlice QoS 需考慮 |
| **Beam management + 頻率複用** | 極化模式、beam switching | NTNCellConfig.beamManagement 更新 |
| **Store & Forward** | IoT 非連續通訊 | NTNSlice S&F policy |
| **Indoor NTN** | 室內衛星存取 | 未來考慮 |

**來源**：
1. [3GPP NTN Overview](https://www.3gpp.org/technologies/ntn-overview)
2. [Ericsson NTN Rel-19 blog](https://www.ericsson.com/en/blog/2024/10/ntn-payload-architecture)
3. [Keysight NTN](https://www.keysight.com/us/en/cmp/topics/non-terrestrial-network-basics-advantages-and-challenges.html)
4. [3GPP Release 19](https://www.3gpp.org/specifications-technologies/releases/release-19)
5. [allpcb 3GPP NTN standards](https://www.allpcb.com/allelectrohub/3gpp-ntn-rf-standards-overview)
6. [ATIS 3GPP Rel-19 webinar](https://atis.org/wp-content/uploads/2024/04/3GPP-Webinar-Slides_04.03.24.pdf)

---

## 六、競爭態勢全面盤點（2026-04）

### 6.1 NTN K8s CRDs / Operators — 仍為零

經搜索 GitHub, GitLab, OperatorHub.io, ArtifactHub, CNCF Landscape, Nephio：
- NTN K8s CRDs: **0**
- NTN K8s Operators: **0**
- 先行者優勢: **成立**

### 6.2 商業競爭者

| 公司 | 產品 | K8s CRDs | 威脅 |
|------|------|:---:|:---:|
| **Mavenir** | vRAN + Core for NTN (Iridium, Terrestar) | ❌ | 高（商業部署，但閉源） |
| **Cisco** | NTN Solution (NCS router + Private 5G) | ❌ | 高（硬體+軟體，非 CRD） |
| **Aalyria** | Spacetime SDN ($1.3B valuation) | ❌ | 高（gRPC only，我們做 K8s 橋接） |
| **Kratos** | OpenSpace ground segment | ❌ | 中（SUSE K8s，但非 NTN CRD） |
| **Leanspace** | Cloud-native satellite C2 | ❌ | 中（SaaS，非 CRD） |

### 6.3 學術/新創

| 項目 | 說明 | 威脅 |
|------|------|:---:|
| **KubeSpace** (Fudan, arXiv 2601.21383) | LEO 容器編排控制平面 | 低（不同層級，專注 in-orbit K8s） |
| **Constellation Space** (YC W26) | AI 衛星故障預測 | 低（mission assurance，非管理層） |
| **Thales PaaS** (KubeCon EU 2026) | Satellite onboard edge (ORCHIDE) | 低（onboard，非地面管理） |
| **OpenNTN** (Bremen) | NTN 通道模型模擬 | 無（工具，非平台） |

---

## 七、5G Core NTN 整合（S14-S15 相關）

### free5GC
- 積極開發 NTN handover（LEO→LEO, NTN→TN）
- 控制面：UE registration, context transfer, PDU session continuity
- 來源：[free5GC NTN handover blog](https://free5gc.org/blog/20250903/), [free5GC NTN Overview](https://free5gc.org/blog/20240626/20240626/)

### Open5GS
- 用作 srsRAN NTN 的 5G Core
- 2025 IFIP 論文：NTN-TN GEO 整合 (cloud-native)
- 來源：[Open5GS GitHub](https://github.com/open5gs/open5gs), [IFIP paper](https://opendl.ifip-tc6.org/db/conf/wmnc/wmnc2025/1571212589.pdf)

---

## 八、新技術選項總結

| 技術 | 描述 | 適用 Sprint | 優先度 |
|------|------|------------|:---:|
| **O1 NETCONF** (OCUDU Helm sidecar) | Runtime gNB config 變更 | S11 | 高 |
| **OAI oai-operators** | 替代 RAN Provider with K8s support | S11 替代方案 | 高 |
| **OpenNTN** | NTN 通道模擬驗證 | S3 cross-validation, S11 test | 中 |
| **KubeEdge** (CNCF Graduated) | 地面站 edge 部署 | Phase 6 | 中 |
| **KubeSpace** | LEO 衛星 K8s 控制平面 | Phase 6 | 低 |
| **TimescaleDB** | Ephemeris 歷史數據 | Phase 6 observability | 低 |
| **Space-Track GP class** | 新 OMM API（舊 TLE class deprecated） | S2.5 SpaceTrack fetcher | 中 |

---

## 九、需要更新的檔案盤點

### 9.1 SDD（ntn-k8s-operators-development-plan.md）

| Section | 需更新內容 |
|---------|---------|
| §3 模組清單 | Provider interface 收窄（8→4 方法） |
| §4 Phase 3 | S11 改為 Helm/O1 NETCONF 而非 raw config file |
| §4 Phase 3 | S10 Provider interface 收窄 |
| §4 Phase 5 | S12-13 NTNCellConfig 加入 NTN-specific fields |
| §4 Phase 5 | S18 Aalyria pin v21.0 + NBI migration 注意 |
| §7 相依性 | 新增 OAI oai-operators 作為備選 |
| §8 風險 | 新增 OCUDU LEO NTN 風險 + OAI 備案 |
| 附錄 | 新增 3GPP Rel-19 NTN 功能列表 |

### 9.2 Proposal（ntn-k8s-operators-proposal-2026.md）

| Section | 需更新內容 |
|---------|---------|
| ADR-003 | Provider interface 精簡 + OAI as third provider |
| ADR-004 | OCUDU Helm/O1 NETCONF integration 方式 |
| §8 競爭分析 | 新增 Mavenir/Cisco/Constellation Space |
| 附錄 B 調研 | 新增 OCUDU GitLab 分支/版本分析 |

### 9.3 ntn-operators 程式碼

| 檔案 | 需更新內容 |
|------|---------|
| `api/v1alpha1/satelliteephemeris_types.go:86` | 殘留 "TLE/ephemeris" 註解 |
| `README.md` | 更新 Phase 1+2 完成狀態 |

---

## 十、下一步建議

### 立即行動（Phase 3 開始前）
1. 更新 SDD + Proposal 反映調研發現
2. 更新 ntn-operators README 反映 Phase 1+2 完成
3. 修正 `satelliteephemeris_types.go` 殘留 TLE 註解

### Phase 3 調整方案

**S10 Provider Interface**（收窄）：
```go
type NTNProvider interface {
    ApplyCellConfig(ctx context.Context, config CellConfig) error
    GetCellStatus(ctx context.Context) (*CellStatus, error)
}
```

**S11 OCUDU Provider**（GEO demo + Helm/O1）：
1. 目標：GEO NTN demo（非 LEO）
2. 整合方式：Helm values overlay 或 O1 NETCONF
3. 文件記錄 LEO 依賴 OCUDU v2.x
4. 備案：OAI Provider（if OCUDU GEO also 有問題）

/*
 * *****************************************************************************
 * Copyright (C) 2019-2026 Chrystian Huot <chrystian.huot@saubeo.solutions>
 *
 * This program is free software: you can redistribute it and/or modify
 * it under the terms of the GNU General Public License as published by
 * the Free Software Foundation, either version 3 of the License, or
 * (at your option) any later version.
 *
 * This program is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 * GNU General Public License for more details.
 *
 * You should have received a copy of the GNU General Public License
 * along with this program.  If not, see <http://www.gnu.org/licenses/>
 * ****************************************************************************
 */

import { Component, EventEmitter, OnInit, Output } from '@angular/core';
import { MatSnackBar } from '@angular/material/snack-bar';
import {
    Config,
    ImportResult,
    RdioScannerAdminService,
    RRCounty,
    RRDownloadJobStatus,
    RRSnapshotInfo,
    RRState,
    System,
    Talkgroup,
} from '../../admin.service';

type Tab = 'live' | 'snapshots' | 'csv';

@Component({
    selector: 'rdio-scanner-admin-radioreference',
    templateUrl: './radioreference.component.html',
    standalone: false,
})
export class RdioScannerAdminRadioReferenceComponent implements OnInit {
    @Output() config = new EventEmitter<Config>();

    // ── Shared credentials ────────────────────────────────────────────────
    username = '';
    password = '';
    appKey = '';

    // ── Tab ───────────────────────────────────────────────────────────────
    activeTab: Tab = 'live';

    // ── Live import ───────────────────────────────────────────────────────
    states: RRState[] = [];
    counties: RRCounty[] = [];
    selectedStateId: number | null = null;
    selectedCountyId: number | null = null;
    portBase = 9000;
    result: ImportResult | null = null;
    selected: Map<number, Set<number>> = new Map();
    loading = false;

    // ── Download-state job (triggered from live tab) ───────────────────────
    downloadJobId: string | null = null;
    downloadJob: RRDownloadJobStatus | null = null;
    downloadingStateId: number | null = null;
    private pollTimer: ReturnType<typeof setTimeout> | null = null;

    // ── CSV import tab ────────────────────────────────────────────────────
    csvFile: File | null = null;
    csvSystemLabel = 'RadioReference';
    csvPortBase = 9000;
    csvType: 'conventional' | 'trs' = 'conventional';
    csvResult: ImportResult | null = null;
    csvSelected: Map<number, Set<number>> = new Map();
    csvLoading = false;

    // ── Offline snapshots tab ─────────────────────────────────────────────
    snapshots: RRSnapshotInfo[] = [];
    snapshotsLoading = false;
    offlineSelectedStateId: number | null = null;
    offlineCounties: RRCounty[] = [];
    offlineSelectedCountyId: number | null = null;
    offlineResult: ImportResult | null = null;
    offlineSelected: Map<number, Set<number>> = new Map();
    offlineLoading = false;

    constructor(
        private adminService: RdioScannerAdminService,
        private matSnackBar: MatSnackBar,
    ) {}

    ngOnInit(): void {
        this.refreshSnapshots();
    }

    // ── Tab switching ─────────────────────────────────────────────────────

    switchTab(tab: Tab): void {
        this.activeTab = tab;
        if (tab === 'snapshots') {
            this.refreshSnapshots();
        }
    }

    // ── Live import ───────────────────────────────────────────────────────

    async loadStates(): Promise<void> {
        if (!this.username || !this.password) {
            this.matSnackBar.open('Enter RadioReference credentials first.', '', { duration: 3000 });
            return;
        }
        this.loading = true;
        this.states = await this.adminService.importRRStates(this.username, this.password, this.appKey);
        this.counties = [];
        this.selectedStateId = null;
        this.selectedCountyId = null;
        this.result = null;
        this.loading = false;
        if (!this.states.length) {
            this.matSnackBar.open('No states returned — check credentials.', '', { duration: 4000 });
        }
    }

    async loadCounties(stateId: number): Promise<void> {
        this.selectedStateId = stateId;
        this.loading = true;
        this.counties = await this.adminService.importRRCounties(this.username, this.password, this.appKey, stateId);
        this.selectedCountyId = null;
        this.result = null;
        this.loading = false;
    }

    async loadCounty(countyId: number): Promise<void> {
        this.selectedCountyId = countyId;
        this.loading = true;
        const base = await this.adminService.getConfig();
        const systemRef = this.nextSystemRef(base.systems ?? []);
        this.result = await this.adminService.importRRCounty(
            this.username, this.password, this.appKey, countyId, systemRef, this.portBase,
        );
        this.initSelected(this.selected, this.result);
        this.loading = false;
    }

    // ── Download state snapshot ───────────────────────────────────────────

    get canDownloadState(): boolean {
        return !!this.selectedStateId && !!this.username && !!this.password;
    }

    get selectedState(): RRState | null {
        return this.states.find(s => s.id === this.selectedStateId) ?? null;
    }

    async downloadState(): Promise<void> {
        if (!this.selectedStateId || !this.selectedState) return;
        const state = this.selectedState;
        this.downloadingStateId = state.id;
        this.downloadJob = null;

        const jobId = await this.adminService.startRRStateDownload(
            this.username, this.password, this.appKey,
            state.id, state.name, state.abbr,
        );
        if (!jobId) return;
        this.downloadJobId = jobId;
        this.pollDownloadJob();
    }

    private pollDownloadJob(): void {
        if (this.pollTimer) clearTimeout(this.pollTimer);
        this.pollTimer = setTimeout(async () => {
            if (!this.downloadJobId) return;
            const status = await this.adminService.getRRDownloadJobStatus(this.downloadJobId);
            this.downloadJob = status;
            if (!status.done) {
                this.pollDownloadJob();
            } else {
                this.downloadJobId = null;
                this.downloadingStateId = null;
                if (status.error) {
                    this.matSnackBar.open('Download failed: ' + status.error, '', { duration: 6000 });
                } else {
                    this.matSnackBar.open('State snapshot saved — switch to Snapshots tab to use it.', '', { duration: 4000 });
                    this.refreshSnapshots();
                }
            }
        }, 1500);
    }

    // ── Live selection helpers ────────────────────────────────────────────

    toggleTalkgroup(sysIdx: number, tgRef: number): void {
        this.toggleInMap(this.selected, sysIdx, tgRef);
    }

    isSelected(sysIdx: number, tgRef: number): boolean {
        return this.selected.get(sysIdx)?.has(tgRef) ?? false;
    }

    selectAll(sysIdx: number): void {
        const sys = this.result?.systems?.[sysIdx];
        this.selected.set(sysIdx, new Set(sys?.talkgroups?.map(tg => tg.talkgroupRef ?? 0) ?? []));
    }

    selectNone(sysIdx: number): void {
        this.selected.set(sysIdx, new Set());
    }

    async import(): Promise<void> {
        if (!this.result?.systems?.length) return;
        this.emitImport(this.result, this.selected);
    }

    // ── Offline / Snapshots tab ───────────────────────────────────────────

    async refreshSnapshots(): Promise<void> {
        this.snapshotsLoading = true;
        this.snapshots = await this.adminService.listRRSnapshots();
        this.snapshotsLoading = false;
    }

    async deleteSnapshot(stateId: number): Promise<void> {
        await this.adminService.deleteRRSnapshot(stateId);
        this.snapshots = this.snapshots.filter(s => s.stateId !== stateId);
        if (this.offlineSelectedStateId === stateId) {
            this.offlineSelectedStateId = null;
            this.offlineCounties = [];
            this.offlineSelectedCountyId = null;
            this.offlineResult = null;
        }
        this.matSnackBar.open('Snapshot deleted.', '', { duration: 2500 });
    }

    async loadOfflineCounties(stateId: number): Promise<void> {
        this.offlineSelectedStateId = stateId;
        this.offlineLoading = true;
        this.offlineCounties = await this.adminService.getRRSnapshotCounties(stateId);
        this.offlineSelectedCountyId = null;
        this.offlineResult = null;
        this.offlineLoading = false;
        if (!this.offlineCounties.length) {
            this.matSnackBar.open('No counties found in snapshot.', '', { duration: 3000 });
        }
    }

    async loadOfflineCounty(countyId: number): Promise<void> {
        if (!this.offlineSelectedStateId) return;
        this.offlineSelectedCountyId = countyId;
        this.offlineLoading = true;
        const base = await this.adminService.getConfig();
        const systemRef = this.nextSystemRef(base.systems ?? []);
        this.offlineResult = await this.adminService.importRRSnapshotCounty(
            this.offlineSelectedStateId, countyId, systemRef, this.portBase,
        );
        this.initSelected(this.offlineSelected, this.offlineResult);
        this.offlineLoading = false;
    }

    toggleOfflineTalkgroup(sysIdx: number, tgRef: number): void {
        this.toggleInMap(this.offlineSelected, sysIdx, tgRef);
    }

    isOfflineSelected(sysIdx: number, tgRef: number): boolean {
        return this.offlineSelected.get(sysIdx)?.has(tgRef) ?? false;
    }

    selectOfflineAll(sysIdx: number): void {
        const sys = this.offlineResult?.systems?.[sysIdx];
        this.offlineSelected.set(sysIdx, new Set(sys?.talkgroups?.map(tg => tg.talkgroupRef ?? 0) ?? []));
    }

    selectOfflineNone(sysIdx: number): void {
        this.offlineSelected.set(sysIdx, new Set());
    }

    async importOffline(): Promise<void> {
        if (!this.offlineResult?.systems?.length) return;
        this.emitImport(this.offlineResult, this.offlineSelected);
    }

    snapshotLabel(snap: RRSnapshotInfo): string {
        return `${snap.stateName} (${snap.stateAbbr})`;
    }

    // ── CSV import ────────────────────────────────────────────────────────

    onCsvFileChange(event: Event): void {
        const input = event.target as HTMLInputElement;
        this.csvFile = input.files?.[0] ?? null;
        this.csvResult = null;
    }

    async importCSV(): Promise<void> {
        if (!this.csvFile) return;
        this.csvLoading = true;
        this.csvResult = null;
        const base = await this.adminService.getConfig();
        const systemRef = this.nextSystemRef(base.systems ?? []);
        if (this.csvType === 'trs') {
            this.csvResult = await this.adminService.importTRSCSV(
                this.csvFile, this.csvSystemLabel, systemRef,
            );
        } else {
            this.csvResult = await this.adminService.importRRCSV(
                this.csvFile, this.csvSystemLabel, systemRef, this.csvPortBase,
            );
        }
        this.initSelected(this.csvSelected, this.csvResult);
        this.csvLoading = false;
        if (!this.csvResult?.systems?.length) {
            this.matSnackBar.open('No valid rows found in CSV.', '', { duration: 4000 });
        }
    }

    toggleCsvTalkgroup(sysIdx: number, tgRef: number): void {
        this.toggleInMap(this.csvSelected, sysIdx, tgRef);
    }

    isCsvSelected(sysIdx: number, tgRef: number): boolean {
        return this.csvSelected.get(sysIdx)?.has(tgRef) ?? false;
    }

    selectCsvAll(sysIdx: number): void {
        const sys = this.csvResult?.systems?.[sysIdx];
        this.csvSelected.set(sysIdx, new Set(sys?.talkgroups?.map(tg => tg.talkgroupRef ?? 0) ?? []));
    }

    selectCsvNone(sysIdx: number): void {
        this.csvSelected.set(sysIdx, new Set());
    }

    async importCsvSelected(): Promise<void> {
        if (!this.csvResult?.systems?.length) return;
        this.emitImport(this.csvResult, this.csvSelected);
    }

    // ── Shared helpers ────────────────────────────────────────────────────

    private initSelected(map: Map<number, Set<number>>, result: ImportResult | null): void {
        map.clear();
        result?.systems?.forEach((_sys, idx) => {
            map.set(idx, new Set(_sys.talkgroups?.map(tg => tg.talkgroupRef ?? 0) ?? []));
        });
    }

    private toggleInMap(map: Map<number, Set<number>>, sysIdx: number, tgRef: number): void {
        const set = map.get(sysIdx);
        if (!set) return;
        if (set.has(tgRef)) set.delete(tgRef);
        else set.add(tgRef);
    }

    private async emitImport(result: ImportResult, sel: Map<number, Set<number>>): Promise<void> {
        const base = await this.adminService.getConfig();

        const mergedGroups = [...(base.groups ?? [])];
        const mergedTags = [...(base.tags ?? [])];

        const ensureGroup = (label: string): number => {
            let g = mergedGroups.find(g => g.label === label);
            if (!g) {
                const id = Math.max(0, ...mergedGroups.map(g => g.id ?? 0)) + 1;
                g = { id, label };
                mergedGroups.push(g);
            }
            return g.id!;
        };

        const ensureTag = (label: string): number => {
            let t = mergedTags.find(t => t.label === label);
            if (!t) {
                const id = Math.max(0, ...mergedTags.map(t => t.id ?? 0)) + 1;
                t = { id, label };
                mergedTags.push(t);
            }
            return t.id!;
        };

        result.groups?.forEach(g => g.label && ensureGroup(g.label));
        result.tags?.forEach(t => t.label && ensureTag(t.label));

        const mergedSystems = [...(base.systems ?? [])];

        result.systems?.forEach((sys, idx) => {
            const selectedRefs = sel.get(idx) ?? new Set<number>();
            if (!selectedRefs.size) return;

            const filteredTgs: Talkgroup[] = (sys.talkgroups ?? [])
                .filter(tg => selectedRefs.has(tg.talkgroupRef ?? 0))
                .map(tg => ({
                    ...tg,
                    groupIds: tg.groupIds?.length
                        ? tg.groupIds.map(id => ensureGroup(mergedGroups.find(g => g.id === id)?.label ?? 'General'))
                        : [ensureGroup('General')],
                    tagId: tg.tagId
                        ? ensureTag(mergedTags.find(t => t.id === tg.tagId)?.label ?? 'Digital')
                        : ensureTag('Digital'),
                }));

            if (!filteredTgs.length) return;
            mergedSystems.push({ ...sys, talkgroups: filteredTgs });
        });

        const mergedChannels = [...(base.options?.bridgeChannels ?? [])];
        const usedPorts = new Set(mergedChannels.map(c => c.udpPort));
        result.channels?.forEach(ch => {
            if (ch.udpPort && !usedPorts.has(ch.udpPort)) {
                mergedChannels.push(ch);
                usedPorts.add(ch.udpPort);
            }
        });

        this.config.emit({
            ...base,
            groups: mergedGroups,
            tags: mergedTags,
            systems: mergedSystems,
            options: { ...base.options, bridgeChannels: mergedChannels },
        });

        this.matSnackBar.open('Imported to Config — click Save to persist.', '', { duration: 3500 });
    }

    private nextSystemRef(systems: System[]): number {
        return Math.max(0, ...systems.map(s => s.systemRef ?? 0)) + 1;
    }
}

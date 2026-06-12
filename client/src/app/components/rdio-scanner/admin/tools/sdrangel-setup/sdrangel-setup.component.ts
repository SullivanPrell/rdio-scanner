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

import { Component, OnDestroy, OnInit } from '@angular/core';
import { MatSnackBar } from '@angular/material/snack-bar';
import {
    BridgeChannelConfig,
    RdioScannerAdminService,
    SDRangelDeviceSetConfig,
    SDRangelProvisionResult,
    SDRangelServiceResult,
    SDRangelServiceStatus,
    SDRangelStatus,
} from '../../admin.service';

@Component({
    selector: 'rdio-scanner-admin-sdrangel-setup',
    templateUrl: './sdrangel-setup.component.html',
    standalone: false,
})
export class RdioScannerAdminSDRangelSetupComponent implements OnInit, OnDestroy {
    // SDRangel API status
    status: SDRangelStatus | null = null;

    // Provision state
    provisionResult: SDRangelProvisionResult | null = null;
    loading = false;

    deviceSetConfigs: SDRangelDeviceSetConfig[] = [];
    bridgeChannels: BridgeChannelConfig[] = [];

    // Service lifecycle state
    serviceStatus: SDRangelServiceStatus | null = null;
    serviceLogs: string[] = [];
    serviceLoading = false;
    showLogs = false;
    showInstallGuide = false;

    private pollTimer: ReturnType<typeof setTimeout> | null = null;

    constructor(
        private adminService: RdioScannerAdminService,
        private matSnackBar: MatSnackBar,
    ) {}

    async ngOnInit(): Promise<void> {
        await this.refresh();
        this.schedulePoll();
    }

    ngOnDestroy(): void {
        if (this.pollTimer) {
            clearTimeout(this.pollTimer);
        }
    }

    // ── Polling ────────────────────────────────────────────────────────────

    private schedulePoll(): void {
        this.pollTimer = setTimeout(async () => {
            this.serviceStatus = await this.adminService.getSDRangelServiceStatus();
            this.schedulePoll();
        }, 10000);
    }

    // ── Full refresh ───────────────────────────────────────────────────────

    async refresh(): Promise<void> {
        this.loading = true;

        const [sdStatus, svcStatus, config] = await Promise.all([
            this.adminService.getSDRangelStatus(),
            this.adminService.getSDRangelServiceStatus(),
            this.adminService.getConfig(),
        ]);

        this.status = sdStatus;
        this.serviceStatus = svcStatus;
        this.bridgeChannels = config.options?.bridgeChannels ?? [];

        // Rebuild device set config list from bridge channels
        const seen = new Set<number>();
        const existing = new Map<number, SDRangelDeviceSetConfig>(
            this.deviceSetConfigs.map(d => [d.index, d])
        );
        const next: SDRangelDeviceSetConfig[] = [];
        for (const ch of this.bridgeChannels) {
            const idx = ch.deviceSetIndex ?? 0;
            if (!seen.has(idx)) {
                seen.add(idx);
                next.push(existing.get(idx) ?? {
                    index: idx,
                    hwType: 'RTLSDR',
                    sequence: idx,
                    centerFrequencyHz: this.suggestCenterFreq(idx),
                    sampleRateHz: 2400000,
                });
            }
        }
        this.deviceSetConfigs = next.sort((a, b) => a.index - b.index);
        this.loading = false;
    }

    // ── Service lifecycle controls ─────────────────────────────────────────

    async startService(): Promise<void> {
        await this.doServiceAction('start', 'sdrangelsrv starting…', 'Failed to start sdrangelsrv');
    }

    async stopService(): Promise<void> {
        await this.doServiceAction('stop', 'sdrangelsrv stopping…', 'Failed to stop sdrangelsrv');
    }

    async restartService(): Promise<void> {
        await this.doServiceAction('restart', 'sdrangelsrv restarting…', 'Failed to restart sdrangelsrv');
    }

    private async doServiceAction(
        action: 'start' | 'stop' | 'restart',
        pendingMsg: string,
        failMsg: string,
    ): Promise<void> {
        this.serviceLoading = true;
        this.matSnackBar.open(pendingMsg, '', { duration: 2000 });

        const result: SDRangelServiceResult = await this.adminService.controlSDRangelService(action);
        this.serviceLoading = false;

        if (result.success) {
            this.matSnackBar.open(result.message, '', { duration: 3000 });
        } else {
            this.matSnackBar.open(failMsg + ': ' + result.message, '', { duration: 5000 });
        }

        // Refresh service status after a brief delay to let Docker act
        setTimeout(async () => {
            this.serviceStatus = await this.adminService.getSDRangelServiceStatus();
        }, 2000);
    }

    async loadLogs(): Promise<void> {
        this.showLogs = true;
        this.serviceLoading = true;
        this.serviceLogs = await this.adminService.getSDRangelServiceLogs();
        this.serviceLoading = false;
    }

    // ── SDRangel provisioning ──────────────────────────────────────────────

    async provision(): Promise<void> {
        if (this.deviceSetConfigs.length === 0) {
            this.matSnackBar.open('No device sets configured — add bridge channels first.', '', { duration: 4000 });
            return;
        }
        this.loading = true;
        this.provisionResult = null;
        this.provisionResult = await this.adminService.provisionSDRangel(this.deviceSetConfigs);
        this.loading = false;
        if (this.provisionResult?.success) {
            this.matSnackBar.open('SDRangel provisioned successfully.', '', { duration: 3000 });
            await this.refresh();
        }
    }

    // ── Helpers ────────────────────────────────────────────────────────────

    channelsForDeviceSet(idx: number): BridgeChannelConfig[] {
        return this.bridgeChannels.filter(ch => (ch.deviceSetIndex ?? 0) === idx);
    }

    /** Suggest a center frequency that covers most channels in the device set. */
    private suggestCenterFreq(dsIdx: number): number {
        const freqs = this.bridgeChannels
            .filter(ch => (ch.deviceSetIndex ?? 0) === dsIdx)
            .map(ch => ch.frequencyHz ?? 0)
            .filter(f => f > 0);
        if (freqs.length === 0) return 462637500;
        const min = Math.min(...freqs);
        const max = Math.max(...freqs);
        return Math.round((min + max) / 2);
    }

    trackByIndex(_i: number, d: SDRangelDeviceSetConfig): number {
        return d.index;
    }

    get serviceStatusColor(): string {
        if (!this.serviceStatus) return 'gray';
        if (this.serviceStatus.running) return '#4caf50';
        return '#f44336';
    }

    get serviceStatusLabel(): string {
        if (!this.serviceStatus) return 'Unknown';
        return this.serviceStatus.running
            ? `Running (${this.serviceStatus.mode})`
            : `Stopped (${this.serviceStatus.mode})`;
    }

    get needsInstall(): boolean {
        if (!this.serviceStatus || this.serviceStatus.running) return false;
        const msg = this.serviceStatus.message ?? '';
        return this.serviceStatus.mode === 'native' && (
            msg.includes('not found') || msg.includes('not in PATH')
        );
    }

    get isDockerMode(): boolean {
        return this.serviceStatus?.mode === 'docker';
    }

    get isNativeMode(): boolean {
        return this.serviceStatus?.mode === 'native';
    }

    formatUptime(seconds?: number): string {
        if (!seconds || seconds <= 0) return '';
        const h = Math.floor(seconds / 3600);
        const m = Math.floor((seconds % 3600) / 60);
        const s = seconds % 60;
        if (h > 0) return `${h}h ${m}m`;
        if (m > 0) return `${m}m ${s}s`;
        return `${s}s`;
    }
}

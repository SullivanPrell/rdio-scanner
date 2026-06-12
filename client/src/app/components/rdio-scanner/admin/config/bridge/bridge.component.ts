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

import { ChangeDetectionStrategy, Component, Input } from '@angular/core';
import { FormArray, FormGroup } from '@angular/forms';
import { RdioScannerAdminService } from '../../admin.service';

@Component({
    selector: 'rdio-scanner-admin-bridge',
    templateUrl: './bridge.component.html',
    standalone: false,
    changeDetection: ChangeDetectionStrategy.OnPush,
})
export class RdioScannerAdminBridgeComponent {
    @Input() form: FormGroup | null = null;

    get channels(): FormArray {
        return this.form?.get('bridgeChannels') as FormArray;
    }

    constructor(private adminService: RdioScannerAdminService) {}

    addChannel(): void {
        this.channels?.push(this.adminService.newBridgeChannelConfigForm());
        this.form?.markAsDirty();
    }

    removeChannel(index: number): void {
        this.channels?.removeAt(index);
        this.form?.markAsDirty();
    }
}

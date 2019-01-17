// Copyright (C) MongoDB, Inc. 2017-present.
//
// Licensed under the Apache License, Version 2.0 (the "License"); you may
// not use this file except in compliance with the License. You may obtain
// a copy of the License at http://www.apache.org/licenses/LICENSE-2.0

package dns

import (
	"errors"
	"fmt"
	"net"
	"runtime"
	"strings"
	"time"
)

type DnsResolver struct {
	// Describe SRV records to add/remove the next time we poll the DNS. Should
	// not be used for purposes other than testing.
	RecordsToAdd    []*net.SRV
	RecordsToRemove []*net.SRV

	// Value for rescanSRVFrequencyMS to pass in when testing. Normally SRV records
	// should not be polled more than once every 60 seconds.
	TestingRescanFrequencyMS time.Duration
}

func (d *DnsResolver) ResolveHostFromSrvRecords(host string) ([]string, error) {
	parsedHosts := strings.Split(host, ",")

	if len(parsedHosts) != 1 {
		return nil, fmt.Errorf("URI with SRV must include one and only one hostname")
	}
	parsedHosts, err := d.fetchSeedlistFromSRV(parsedHosts[0])
	if err != nil {
		return nil, err
	}
	return parsedHosts, nil
}

func (d *DnsResolver) ResolveAdditionalQueryParametersFromTxtRecords(host string) ([]string, error) {
	var connectionArgsFromTXT []string

	// error ignored because not finding a TXT record should not be
	// considered an error.
	recordsFromTXT, _ := net.LookupTXT(host)

	// This is a temporary fix to get around bug https://github.com/golang/go/issues/21472.
	// It will currently incorrectly concatenate multiple TXT records to one
	// on windows.
	if runtime.GOOS == "windows" {
		recordsFromTXT = []string{strings.Join(recordsFromTXT, "")}
	}

	if len(recordsFromTXT) > 1 {
		return nil, errors.New("multiple records from TXT not supported")
	}
	if len(recordsFromTXT) > 0 {
		connectionArgsFromTXT = strings.FieldsFunc(recordsFromTXT[0], func(r rune) bool { return r == ';' || r == '&' })

		err := validateTXTResult(connectionArgsFromTXT)
		if err != nil {
			return nil, err
		}
	}

	return connectionArgsFromTXT, nil
}

func (d *DnsResolver) fetchSeedlistFromSRV(host string) ([]string, error) {
	var err error

	_, _, err = net.SplitHostPort(host)

	if err == nil {
		// we were able to successfully extract a port from the host,
		// but should not be able to when using SRV
		return nil, fmt.Errorf("URI with srv must not include a port number")
	}

	_, addresses, err := net.LookupSRV("mongodb", "tcp", host)
	if err != nil {
		return nil, err
	}

	// Add/remove records to mimic changing the DNS records. Used only in testing.
	if d.RecordsToAdd != nil {
		addresses = append(addresses, d.RecordsToAdd...)
	}
	if d.RecordsToRemove != nil {
		for _, removeAddr := range d.RecordsToRemove {
			for j, addr := range addresses {
				if removeAddr.Target == addr.Target && removeAddr.Port == addr.Port {
					addresses = append(addresses[:j], addresses[j+1:]...)
				}
			}
		}
	}

	parsedHosts := make([]string, len(addresses))
	for i, address := range addresses {
		trimmedAddressTarget := strings.TrimSuffix(address.Target, ".")
		err := validateSRVResult(trimmedAddressTarget, host)
		if err != nil {
			return nil, err
		}
		parsedHosts[i] = fmt.Sprintf("%s:%d", trimmedAddressTarget, address.Port)
	}
	return parsedHosts, nil
}

func validateSRVResult(recordFromSRV, inputHostName string) error {
	separatedInputDomain := strings.Split(inputHostName, ".")
	separatedRecord := strings.Split(recordFromSRV, ".")
	if len(separatedRecord) < 2 {
		return errors.New("DNS name must contain at least 2 labels")
	}
	if len(separatedRecord) < len(separatedInputDomain) {
		return errors.New("Domain suffix from SRV record not matched input domain")
	}

	inputDomainSuffix := separatedInputDomain[1:]
	domainSuffixOffset := len(separatedRecord) - (len(separatedInputDomain) - 1)

	recordDomainSuffix := separatedRecord[domainSuffixOffset:]
	for ix, label := range inputDomainSuffix {
		if label != recordDomainSuffix[ix] {
			return errors.New("Domain suffix from SRV record not matched input domain")
		}
	}
	return nil
}

var allowedTXTOptions = map[string]struct{}{
	"authsource": {},
	"replicaset": {},
}

func validateTXTResult(paramsFromTXT []string) error {
	for _, param := range paramsFromTXT {
		kv := strings.SplitN(param, "=", 2)
		if len(kv) != 2 {
			return errors.New("Invalid TXT record")
		}
		key := strings.ToLower(kv[0])
		if _, ok := allowedTXTOptions[key]; !ok {
			return fmt.Errorf("Cannot specify option '%s' in TXT record", kv[0])
		}
	}
	return nil
}
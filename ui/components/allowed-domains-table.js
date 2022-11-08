export default function AllowedDomainsTable({ domains, onRemove }) {
    function domainRows() {
        let rows = []
        domains.forEach(domain => {
            rows.push(
                <tr className="group truncate">
                    <th scope="row" className="text-sm">
                        {domain}
                    </th>
                    <th scope="row">
                        <button
                            className='p-1 text-2xs text-gray-500/75 hover:text-gray-600 my-1'
                            onClick={() => {onRemove(domain)}}
                        >
                            Remove
                        </button>
                    </th>
                </tr>
            )
        })
        return rows
    }

    return (
        <>
        {domains.length > 0 && (
        <div className="overflow-x-auto rounded-lg border border-gray-200/75">
                <table className="w-full text-sm text-gray-600">
                    <thead className='border-b border-gray-200/75 bg-zinc-50/50 text-xs text-gray-500'>
                        <tr>
                            <th scope="col" className="w-auto py-2 px-5 font-medium first:max-w-[40%]">
                                Domain
                            </th>
                            <th scope="col"></th>
                        </tr>
                    </thead>
                    <tbody className='divide-y divide-gray-100'>
                        {domainRows()}
                    </tbody>
                </table>
        </div>
        )}
        </>
    )
}
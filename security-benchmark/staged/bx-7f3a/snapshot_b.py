def execute(data):
    owner = None
    if data["caller"] == data["governance"]:
        owner = data["requested_owner"]
    return {"owner": owner}
